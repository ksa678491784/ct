package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	C "github.com/metacubex/mihomo/constant"
)

type listFetcher struct {
	cache listFetcherCache
	cfg   config
	ctx   context.Context
}

type listFetcherCache struct {
	dir        string
	forceCache bool
	ttl        time.Duration
}

func newListFetcher(ctx context.Context, cfg config, forceCache bool) listFetcher {
	return listFetcher{
		listFetcherCache{
			cfg.Cache.Dir,
			forceCache,
			time.Duration(cfg.Cache.Ttl) * time.Second,
		},
		cfg,
		ctx,
	}
}

func (lf listFetcher) fetchLists() map[string][]C.Proxy {
	type result struct {
		url     string
		proxies []C.Proxy
		err     error
	}
	results := make(chan result)
	wg := sync.WaitGroup{}

	timeout := time.Duration(lf.cfg.FetchTimeout) * time.Millisecond
	sources := make(map[string][]C.Proxy)

	for _, l := range lf.cfg.Layers {
		for _, s := range l.Sources {
			if _, ok := sources[s.Url]; ok {
				continue
			}
			sources[s.Url] = nil

			wg.Add(1)
			go func(src sourceConfig) {
				defer wg.Done()
				proxies, err := lf.fetchUrl(src.Url, timeout, s.TlsSkip)
				results <- result{url: src.Url, proxies: proxies, err: err}
			}(s)
		}
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			delete(sources, r.url)
			logger.errorf("%v: %v", r.url, r.err)
			continue
		}
		sources[r.url] = r.proxies
	}
	return sources
}

func (lf *listFetcher) fetchUrl(
	url string,
	timeout time.Duration,
	tlsSkip bool,
) ([]C.Proxy, error) {
	local_file, is_local_file := strings.CutPrefix(url, "file://")

	if !is_local_file {
		if proxies := lf.cache.load(url); proxies != nil {
			return proxies, nil
		}
	}

	buf := []byte{}
	err := error(nil)

	logger.debugf("Loading %v..", url)
	if is_local_file {
		buf, err = os.ReadFile(local_file)
	} else {
		resp := &http.Response{}
		if resp, err = (httpReq{
			ctx:      lf.ctx,
			skipCert: tlsSkip,
			timeout:  timeout,
			url:      url,
		}).doHttpReq(); err != nil {
			return nil, fmt.Errorf("HTTP error: %v", err)
		} else {
			defer resp.Body.Close()
			buf, err = io.ReadAll(resp.Body)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("read error: %v", err)
	}

	if parser, err := provider.NewProxiesParserSimple(url); err != nil {
		return nil, fmt.Errorf("parse error: %v", err)
	} else {
		logger.debugf("Parsing %v..", url)
		if proxies, err := parser(buf); err != nil {
			return nil, fmt.Errorf("parse error: %v", err)
		} else {
			if !is_local_file {
				if err := lf.cache.save(url, buf); err != nil {
					logger.errorf("Failed to save cache for %v: %v", url, err)
				}
			}
			return proxies, nil
		}
	}
}

type cachedProxies struct {
	Timestamp time.Time `json:"timestamp"`
	Data      []byte    `json:"data"`
}

func (lfc *listFetcherCache) filePath(url string) string {
	h := sha256.New()
	h.Write([]byte(url))
	hash := fmt.Sprintf("%x", h.Sum(nil))
	return filepath.Join(lfc.dir, hash+".json")
}

func (lfc *listFetcherCache) load(url string) []C.Proxy {
	if lfc.dir == "" {
		return nil
	}

	cached := cachedProxies{}

	if data, err := os.ReadFile(lfc.filePath(url)); err != nil {
		return nil
	} else if err := json.Unmarshal(data, &cached); err != nil {
		return nil
	}

	if !lfc.forceCache && time.Since(cached.Timestamp) > lfc.ttl {
		return nil
	}

	if parser, err := provider.NewProxiesParserSimple(url); err != nil {
		return nil
	} else if proxies, err := parser(cached.Data); err != nil {
		return nil
	} else {
		logger.debugf("Cache hit for %v", url)
		return proxies
	}
}

func (lfc *listFetcherCache) save(url string, data []byte) error {
	if lfc.dir == "" {
		return nil
	}

	if _, err := os.Stat(lfc.dir); os.IsNotExist(err) {
		if err := os.MkdirAll(lfc.dir, 0755); err != nil {
			logger.errorf("Failed to create cache dir: %v", err)
			lfc.dir = ""
			return nil
		}
	}

	cached := cachedProxies{
		Timestamp: time.Now(),
		Data:      data,
	}
	if jsonData, err := json.Marshal(cached); err == nil {
		return os.WriteFile(lfc.filePath(url), jsonData, 0644)
	} else {
		return err
	}
}
