package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	C "github.com/metacubex/mihomo/constant"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var handler = newLogHandler()
var logger = newLogger(&handler)

type stats struct {
	total int
	uniq  int
}

func (s stats) String() string {
	return fmt.Sprintf("%v total, %v unique", s.total, s.uniq)
}

func calcSourcesStats(sources map[string][]C.Proxy) stats {
	p := []C.Proxy{}
	for _, proxies := range sources {
		p = append(p, proxies...)
	}
	return stats{len(p), len(uniq(p, func(i C.Proxy) string { return i.MappingHash() }))}
}

func usage() {
	logger.errorf("Usage: %v gen", os.Args[0])
	logger.errorf("Usage: %v run [-force-cache] <config file>", os.Args[0])
	logger.errorf("Usage: %v run [-force-cache] -json <config file>", os.Args[0])
	logger.errorf("Usage: %v run [-force-cache] -mihomo <config file>", os.Args[0])
	logger.errorf("Usage: %v run [-force-cache] -mihomo-complete <config file>", os.Args[0])
	os.Exit(1)
}

type outfmt struct {
	json       bool
	mihomo     bool
	mihomoComp bool
}

func (o *outfmt) flagCount() int8 {
	return bool2int(o.json) +
		bool2int(o.mihomo) +
		bool2int(o.mihomoComp)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	pa := struct {
		config     string
		forceCache bool
		out        outfmt
	}{}

	switch os.Args[1] {
	case "gen":
		if config, err := yaml.Marshal(defaultConfig()); err != nil {
			logger.errorf("Failed to marshal config: %v", err)
		} else {
			fmt.Print(string(config))
		}
		return
	case "run":
		flag.BoolVar(&pa.forceCache, "force-cache", false, "Use cache even if it's expired")
		flag.BoolVar(&pa.out.json, "json", false, "Output json")
		flag.BoolVar(&pa.out.mihomo, "mihomo", false, "Output mihomo config fragment")
		flag.BoolVar(&pa.out.mihomoComp, "mihomo-complete", false, "Output complete mihomo config")

		flag.CommandLine.Parse(os.Args[2:])
		args := flag.Args()

		if len(args) != 1 || pa.out.flagCount() > 1 {
			usage()
		}
		pa.config = args[0]
	default:
		usage()
	}

	// Suppress mihomo warnings
	logrus.SetLevel(logrus.ErrorLevel)
	logrus.SetFormatter(&handler)
	logrus.SetOutput(os.Stderr)

	if pa.out.flagCount() > 0 {
		// Let's write resulting json to stdout instead
		handler.logSuccess2Stderr()
	}

	cfg := config{}

	if data, err := os.ReadFile(pa.config); err != nil {
		logger.errorf("Failed to read config: %v", err)
		os.Exit(1)
	} else {
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err = decoder.Decode(&cfg); err != nil {
			logger.errorf("Failed to parse config: %v", err)
			os.Exit(1)
		}
	}

	if cfg, err := yaml.Marshal(cfg); err == nil {
		logger.debugf("Config:\n%v", strings.TrimSuffix(string(cfg), "\n"))
	}

	if len(cfg.Layers) == 0 {
		logger.errorf("No proxy layers configured")
		os.Exit(1)
	}

	for li, l := range cfg.Layers {
		if len(l.Sources) == 0 {
			logger.errorf("No sources found for layer %v", li+1)
			os.Exit(1)
		}
	}

	ctx := context.Background()

	logger.infof("Loading sources..")
	sources := newListFetcher(ctx, cfg, pa.forceCache).fetchLists()
	if len(sources) == 0 {
		logger.errorf("No sources loaded")
		os.Exit(1)
	}
	logger.successf("Loaded %v sources (%v)", len(sources), calcSourcesStats(sources))

	layers := make([][]C.Proxy, len(cfg.Layers))
	for li, l := range cfg.Layers {
		layer := &layers[li]
		for _, s := range l.Sources {
			if proxies, ok := sources[s.Url]; ok {
				*layer = append(*layer, proxies...)
			}
		}
		if len(*layer) == 0 {
			logger.errorf("No sources fetched for layer %v", li+1)
			os.Exit(1)
		}
		*layer = uniq(
			*layer,
			func(p C.Proxy) string {
				return p.MappingHash()
			},
		)
		logger.successf("%v proxies on layer %v", len(*layer), li+1)
	}

	result := newChainTester(cfg, ctx, layers).test()
	if pa.out.json {
		layerProxyLines := [][][]string{}
		for layer := range layers {
			layerProxyLines = append(layerProxyLines, result[layer].proxyLines())
		}

		if json, err := json.Marshal(layerProxyLines); err != nil {
			logger.errorf("Failed to serialize to json: %v", err)
			os.Exit(1)
		} else {
			fmt.Println(string(json))
		}
	} else if pa.out.mihomo || pa.out.mihomoComp {
		output := struct {
			Proxies []map[string]any
			Groups  []map[string]any
		}{}

		for layer := range layers {
			op, og := result[layer].mihomoConfig(&cfg.Layers[layer])
			output.Proxies = append(output.Proxies, op...)
			output.Groups = append(output.Groups, og)
		}

		outputMap := map[string]any{
			"proxies":      output.Proxies,
			"proxy-groups": output.Groups,
		}

		if pa.out.mihomoComp {
			for key, output := range outputMap {
				if skel, ok := cfg.MihomoSkel[key].([]any); ok {
					if output, ok := output.([]map[string]any); ok {
						cfg.MihomoSkel[key] = append(unpackArray(output), skel...)
					}
				} else {
					cfg.MihomoSkel[key] = output
				}
			}
			outputMap = cfg.MihomoSkel
		}

		if yaml, err := yaml.Marshal(outputMap); err != nil {
			logger.errorf("Failed to serialize to yaml: %v", err)
			os.Exit(1)
		} else {
			fmt.Println(string(yaml))
		}
	}

	if len(last(result).proxyLines()) == 0 {
		os.Exit(2)
	}
}
