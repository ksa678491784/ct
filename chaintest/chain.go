package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	T "github.com/metacubex/mihomo/tunnel"
)

type chains []chain

func (c chains) Len() int           { return len(c) }
func (c chains) Swap(i, j int)      { c[i], c[j] = c[j], c[i] }
func (c chains) Less(i, j int) bool { return c[i].latency < c[j].latency }

func (c *chains) proxyLines() [][]string {
	proxyLines := [][]string{}
	for _, ps := range *c {
		lines := []string{}
		for _, p := range ps.proxies {
			lines = append(lines, p.ProxyLine())
		}
		proxyLines = append(proxyLines, lines)
	}
	return proxyLines
}

func (c *chains) mihomoConfig(lc *layerConfig) ([]map[string]any, map[string]any) {
	op := []map[string]any{}
	proxies := []string{}
	for _, ps := range *c {
		last := *last(ps.proxies)
		op = append(op, last.Mapping())
		proxies = append(proxies, last.Name())
	}

	og := map[string]any{"name": lc.Name}
	maps.Copy(og, lc.Mihomo.ProxyGroupsProps)
	if skelProxies, exists := og["proxies"].([]any); exists {
		og["proxies"] = append(unpackArray(proxies), skelProxies...)
	} else {
		og["proxies"] = proxies
	}
	return op, og
}

type chainTester struct {
	cfg         config
	checkStatus utils.IntRanges[uint16]
	ctx         context.Context
	layers      [][]C.Proxy
}

func newChainTester(cfg config, ctx context.Context, layers [][]C.Proxy) chainTester {
	return chainTester{
		cfg:    cfg,
		ctx:    ctx,
		layers: layers,
	}
}

type chain struct {
	proxies []C.Proxy
	latency uint16
}

func (ct chainTester) test() []chains {
	working := make([]chains, len(ct.layers))
	registered := make(map[string]C.Proxy)
	hash2name := make(map[string]string)
	for li := range ct.layers {
		cfgl := ct.cfg.Layers[li]
		logger.infof("=== Testing layer %v (n=%v, timeout=%vms) ===", li+1, cfgl.N, cfgl.Check.Timeout)

		prevLayer := []chain(nil)
		if li != 0 {
			prevLayer = working[li-1]
		}

		workingl := &working[li]
		*workingl = ct.testLayer(li, prevLayer, &hash2name)
		sort.Sort(*workingl)
		if len(*workingl) == 0 {
			logger.errorf("No working proxies in layer %v", li+1)
			return working
		}

		logger.successf("Layer %v: %v working", li+1, len(*workingl))
		printChain(*workingl, hash2name)

		for _, w := range *workingl {
			last := *last(w.proxies)
			registered[last.Name()] = last
		}
		tunnel.UpdateProxies(registered, nil)
	}
	return working
}

func printChain(chains []chain, hash2name map[string]string) {
	for _, c := range chains {
		logger.successf("  - %v (%vms)", hash2name[(*last(c.proxies)).MappingHash()], c.latency)
		for pi, p := range c.proxies {
			logger.successf("    %v: %v", pi+1, p.ProxyLine())
		}
	}
}

func makeProxyChain(proxy C.Proxy, name string, dialer *string) (C.Proxy, error) {
	m := make(map[string]any)
	maps.Copy(m, proxy.Mapping())
	if dialer != nil {
		m["dialer-proxy"] = *dialer
	}
	m["name"] = name
	return adapter.ParseProxy(m, adapter.WithTunnelForAPI(T.Tunnel), adapter.WithProxyLine(proxy.ProxyLine()))
}

func (ct chainTester) testLayer(layer int, prev []chain, hash2name *map[string]string) []chain {
	ctx, cancel := context.WithCancel(ct.ctx)
	defer cancel()

	lc := ct.cfg.Layers[layer]
	wg := sync.WaitGroup{}
	tester := singleProxyTester{
		check:   lc.Check.Check,
		ctx:     ctx,
		results: make(chan singleProxyTesterRes, lc.N),
		sem:     make(chan struct{}, ct.cfg.Concurrency),
		sucEps:  nil,
		sucEpsM: sync.Mutex{},
		timeout: time.Duration(lc.Check.Timeout) * time.Millisecond,
		wg:      &wg,
	}

	if lc.Check.SkipSameEndpoint {
		tester.sucEps = map[string]bool{}
	}

	proxies := ct.layers[layer]
	if prev == nil {
		total := len(proxies)
		for i, orig := range proxies {
			new, err := makeProxyChain(orig, orig.MappingHash(), nil)
			if err != nil {
				logger.errorf("Failed to create proxy chain: %v", err)
				continue
			}
			(*hash2name)[new.MappingHash()] = orig.Name()

			wg.Add(1)
			go tester.test([]C.Proxy{}, new, orig.Name(), i+1, total)
		}
	} else {
		total := len(proxies) * len(prev)
		i := 1
		for _, orig := range proxies {
			for _, pc := range prev {
				last := *last(pc.proxies)
				lastName := last.Name()

				new, err := makeProxyChain(
					orig,
					fmt.Sprintf("%v->%v", lastName, orig.MappingHash()),
					&lastName,
				)
				if err != nil {
					logger.errorf("Failed to create proxy chain: %v", err)
					continue
				}
				name := fmt.Sprintf("%v -> %v", (*hash2name)[last.MappingHash()], orig.Name())
				(*hash2name)[new.MappingHash()] = name

				wg.Add(1)
				go tester.test(pc.proxies, new, name, i, total)
				i++
			}
		}
	}

	go func() {
		wg.Wait()
		close(tester.results)
	}()

	chain := []chain{}
	endpoints := map[string]struct{}{}
	for r := range tester.results {
		if r.skipped {
			continue
		}

		lastProxy := *last(r.chain.proxies)
		name := (*hash2name)[lastProxy.MappingHash()]
		addr := lastProxy.Addr()

		if r.err != nil {
			logger.errorf("Failed %v (%v): %v", addr, name, r.err)
			continue
		}
		if r.timeout {
			logger.errorf("Timed out %v (%v): %vms", addr, name, r.chain.latency)
			continue
		}

		if _, exists := endpoints[addr]; lc.Check.SkipSameEndpoint && exists {
			logger.warnf("Skipping %v (%v): already added", addr, name)
			continue
		}
		endpoints[addr] = struct{}{}

		logger.successf("Ok %v (%v): %dms", addr, name, r.chain.latency)

		chain = append(chain, r.chain)
		if len(chain) >= int(lc.N) {
			logger.infof("Found %v working proxies, stopping", lc.N)
			break
		}
	}
	return chain
}

type singleProxyTesterRes struct {
	chain   chain
	err     error
	skipped bool
	timeout bool
}

type singleProxyTester struct {
	check   check
	ctx     context.Context
	results chan singleProxyTesterRes
	sem     chan struct{}
	sucEps  map[string]bool
	sucEpsM sync.Mutex
	timeout time.Duration
	wg      *sync.WaitGroup
}

func (tp *singleProxyTester) test(c []C.Proxy, p C.Proxy, name string, cur int, total int) {
	defer tp.wg.Done()

	addr := p.Addr()
	if len(c) > 0 && (*last(c)).Addr() == addr {
		logger.warnf("[%v/%v] Skipping %v (%v): matches previous layer", cur, total, addr, name)
		tp.results <- singleProxyTesterRes{
			chain:   chain{},
			err:     nil,
			skipped: true,
			timeout: false,
		}
		return
	}

	tp.sem <- struct{}{}
	defer func() { <-tp.sem }()

	select {
	case <-tp.ctx.Done():
		return
	default:
	}

	if tp.sucEps != nil {
		skip := func() bool {
			tp.sucEpsM.Lock()
			defer tp.sucEpsM.Unlock()
			if val, exists := tp.sucEps[addr]; !exists {
				tp.sucEps[addr] = false
				return false
			} else {
				return val
			}
		}()

		if skip {
			logger.warnf("[%v/%v] Skipping %v (%v): already added", cur, total, addr, name)
			tp.results <- singleProxyTesterRes{
				chain:   chain{},
				err:     nil,
				skipped: true,
				timeout: false,
			}
			return
		}
	}

	logger.infof("[%v/%v] Testing %v (%v)", cur, total, addr, name)
	latency, err := tp.checkProxy(&p, tp.check)
	if latency >= uint16(tp.timeout.Milliseconds()) {
		tp.results <- singleProxyTesterRes{
			chain:   chain{append(c, p), latency},
			err:     err,
			skipped: false,
			timeout: true,
		}
		return
	}

	if err == nil && tp.sucEps != nil {
		func() {
			tp.sucEpsM.Lock()
			defer tp.sucEpsM.Unlock()
			tp.sucEps[addr] = true
		}()
	}

	tp.results <- singleProxyTesterRes{
		chain:   chain{append(c, p), latency},
		err:     err,
		skipped: false,
		timeout: false,
	}
}

func (tp *singleProxyTester) checkProxy(p *C.Proxy, c check) (uint16, error) {
	if c.Url != "" {
		return tp.checkLeaf(p, c)
	}

	if len(c.And) > 0 {
		return tp.checkAnd(p, c.And)
	}

	if len(c.Or) > 0 {
		return tp.checkOr(p, c.Or)
	}

	// normally unreachable
	return 0, fmt.Errorf("empty check node")
}

func (tp *singleProxyTester) checkLeaf(p *C.Proxy, c check) (uint16, error) {
	ctx, cancel := context.WithTimeout(tp.ctx, tp.timeout)
	defer cancel()

	if c.Regex == "" {
		if cs, err := utils.NewUnsignedRanges[uint16](fmt.Sprintf("%v", c.Status)); err != nil {
			return 0, err
		} else {
			return (*p).URLTest(ctx, c.Url, cs)
		}
	} else if body, latency, err := fetchBody(ctx, p, c.Url, c.Status, tp.timeout); err != nil {
		return 0, err
	} else {
		if m, err := c.regex.FindStringMatch(string(body)); err != nil {
			return 0, fmt.Errorf("regex mismatch: %v", err)
		} else if m == nil {
			return 0, fmt.Errorf("regex mismatch")
		}
		return latency, nil
	}
}

func (tp *singleProxyTester) checkAnd(p *C.Proxy, checks []check) (uint16, error) {
	maxLat := uint16(0)
	for _, sub := range checks {
		if lat, err := tp.checkProxy(p, sub); err != nil {
			return 0, err
		} else {
			maxLat = max(lat, maxLat)
		}
	}
	return maxLat, nil
}

func (tp *singleProxyTester) checkOr(p *C.Proxy, checks []check) (uint16, error) {
	for _, sub := range checks {
		if lat, err := tp.checkProxy(p, sub); err == nil {
			return lat, nil
		}
	}
	return 0, fmt.Errorf("all sub-checks failed")
}

func fetchBody(
	ctx context.Context,
	p *C.Proxy,
	targetUrl string,
	targetStatus int,
	timeout time.Duration,
) ([]byte, uint16, error) {
	metadata := C.Metadata{}
	if parsedURL, err := url.Parse(targetUrl); err != nil {
		return nil, 0, err
	} else {
		port := parsedURL.Port()
		if port == "" {
			switch parsedURL.Scheme {
			case "https":
				port = "443"
			case "http":
				port = "80"
			default:
				return nil, 0, fmt.Errorf("%v scheme not supported", targetUrl)
			}
		}

		if err := metadata.SetRemoteAddress(net.JoinHostPort(parsedURL.Hostname(), port)); err != nil {
			return nil, 0, err
		}
	}

	start := time.Now()
	conn, err := (*p).DialContext(ctx, &metadata)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()

	resp, err := httpReq{
		ctx:      ctx,
		skipCert: true,
		timeout:  timeout,
		url:      targetUrl,
	}.doHttpReq(func(c *http.Client, t *http.Transport) {
		t.DialContext = func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return conn, nil
		}
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	})

	if err != nil {
		return nil, 0, err
	} else {
		defer resp.Body.Close()
		if body, err := io.ReadAll(io.LimitReader(resp.Body, 65536)); err != nil {
			return nil, 0, err
		} else if resp.StatusCode != targetStatus {
			return nil, 0, fmt.Errorf("unexpected status %v", resp.StatusCode)
		} else {
			return body, uint16(time.Since(start).Milliseconds()), nil
		}
	}
}
