package main

import (
	"fmt"

	"github.com/dlclark/regexp2"
)

type config struct {
	Cache        cacheConfig    `yaml:"cache"`
	Concurrency  int            `yaml:"concurrency"`
	FetchTimeout uint16         `yaml:"fetch-timeout"`
	Layers       []layerConfig  `yaml:"layers"`
	MihomoSkel   map[string]any `yaml:"mihomo-output"`
}

type cacheConfig struct {
	Dir string `yaml:"dir"`
	Ttl uint32 `yaml:"ttl-sec"`
}

type layerConfig struct {
	Check   checkConfig       `yaml:"check"`
	Mihomo  mihomoLayerConfig `yaml:"mihomo-output"`
	N       uint32            `yaml:"n"`
	Name    string            `yaml:"name"`
	Sources []sourceConfig    `yaml:"sources"`
}

type check struct {
	And    []check `yaml:"and,omitempty"`
	Or     []check `yaml:"or,omitempty"`
	Regex  string  `yaml:"regex,omitempty"`
	Status int     `yaml:"status"`
	Url    string  `yaml:"url"`
	regex  *regexp2.Regexp
}

type checkConfig struct {
	Check            check  `yaml:"check,inline"`
	SkipSameEndpoint bool   `yaml:"skip-same-endpoint"`
	Timeout          uint16 `yaml:"timeout"`
}

type userCheckConfig struct {
	And              []check `yaml:"and"`
	Or               []check `yaml:"or"`
	Regex            string  `yaml:"regex"`
	SkipSameEndpoint bool    `yaml:"skip-same-endpoint"`
	Status           int     `yaml:"status"`
	Timeout          uint16  `yaml:"timeout"`
	Url              string  `yaml:"url"`
}

func (C *check) handleCheck(depth int) error {
	if depth == 7 {
		return fmt.Errorf("max depth reached")
	}

	if bool2int(len(C.And) != 0)+bool2int(len(C.Or) != 0)+bool2int(C.Regex != "" || C.Url != "" || C.Status != 0) != 1 {
		return fmt.Errorf("must contain either 'and', 'or', or a single check with 'url' and 'status' (optional 'regex')")
	}

	if len(C.And) == 0 && len(C.Or) == 0 {
		if C.Status == 0 {
			return fmt.Errorf("'status' must be filled")
		}
		if C.Url == "" {
			return fmt.Errorf("'url' must be filled")
		}
	}

	if C.Regex != "" {
		if compiled, err := regexp2.Compile(C.Regex, regexp2.None); err != nil {
			return fmt.Errorf("invalid regex %v: %v", C.Regex, err)
		} else {
			C.regex = compiled
		}
	}

	for _, l := range [][]check{C.And, C.Or} {
		for i := range l {
			if err := l[i].handleCheck(depth + 1); err != nil {
				return err
			}
		}
	}

	return nil
}

func (out *checkConfig) UnmarshalYAML(fn func(any) error) error {
	u := userCheckConfig{}
	if err := fn(&u); err != nil {
		return err
	}

	out.Check = check{
		And:    u.And,
		Or:     u.Or,
		Regex:  u.Regex,
		Status: u.Status,
		Url:    u.Url,
	}
	out.SkipSameEndpoint = u.SkipSameEndpoint
	out.Timeout = u.Timeout

	if err := out.Check.handleCheck(0); err != nil {
		return err
	}
	return nil
}

type mihomoLayerConfig struct {
	ProxyGroupsProps map[string]any `yaml:"proxy-group-props"`
}

type sourceConfig struct {
	TlsSkip bool   `yaml:"tls-skip"`
	Url     string `yaml:"url"`
}

func (out *sourceConfig) UnmarshalYAML(fn func(any) error) error {
	str := ""
	if err := fn(&str); err == nil {
		out.TlsSkip = false
		out.Url = str
		return nil
	}

	type S sourceConfig
	s := S{}
	if err := fn(&s); err != nil {
		return err
	}
	*out = sourceConfig(s)
	return nil
}

func (out *config) UnmarshalYAML(fn func(any) error) error {
	type C config
	c := C{}
	if err := fn(&c); err != nil {
		return err
	}

	*out = config(c)
	def := defaultConfig()

	if out.Concurrency == 0 {
		out.Concurrency = def.Concurrency
	}
	if out.FetchTimeout == 0 {
		out.FetchTimeout = def.FetchTimeout
	}
	if out.Cache.Ttl == 0 {
		out.Cache.Ttl = def.Cache.Ttl
	}

	defLayer := def.Layers[0]
	for li := range out.Layers {
		layer := &out.Layers[li]
		if layer.Check.Timeout == 0 {
			layer.Check.Timeout = defLayer.Check.Timeout
		}
		if layer.Mihomo.ProxyGroupsProps == nil {
			layer.Mihomo.ProxyGroupsProps = defLayer.Mihomo.ProxyGroupsProps
		}
		if layer.N == 0 {
			layer.N = defLayer.N
		}
		if layer.Name == "" {
			layer.Name = fmt.Sprintf("Layer %v", li+1)
		}
	}
	return nil
}

func defaultConfig() config {
	return config{
		Cache: cacheConfig{
			Dir: ".ct-cache",
			Ttl: 3600,
		},
		Concurrency:  10,
		FetchTimeout: 2000,
		Layers: []layerConfig{
			{
				Check: checkConfig{
					Check: check{
						Status: 204,
						Url:    "https://www.gstatic.com/generate_204",
					},
					SkipSameEndpoint: true,
					Timeout:          200,
				},
				Mihomo: mihomoLayerConfig{
					map[string]any{
						"type": "url-test",
					},
				},
				N:    3,
				Name: "Layer 1",
				Sources: []sourceConfig{
					{
						TlsSkip: false,
						Url:     "https://example.com/subscription.txt",
					},
				},
			},
			{
				Check: checkConfig{
					Check: check{
						And: []check{
							{
								Status: 204,
								Url:    "https://www.gstatic.com/generate_204",
							},
							{
								Regex:  "loc=NL",
								Status: 200,
								Url:    "https://icanhazip.com/cdn-cgi/trace",
							},
						},
					},
					SkipSameEndpoint: true,
					Timeout:          200,
				},
				Mihomo: mihomoLayerConfig{
					map[string]any{
						"type": "url-test",
					},
				},
				N:    5,
				Name: "Layer 2",
				Sources: []sourceConfig{
					{
						TlsSkip: false,
						Url:     "https://example.com/subscription.txt",
					},
				},
			},
		},
		MihomoSkel: map[string]any{},
	}
}
