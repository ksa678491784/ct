package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"
)

type httpReq struct {
	ctx      context.Context
	skipCert bool
	timeout  time.Duration
	url      string
}

func (r httpReq) doHttpReq(customize ...func(*http.Client, *http.Transport)) (*http.Response, error) {
	client := http.Client{
		Timeout: r.timeout,
	}
	transport := http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: r.skipCert,
		},
	}
	for _, c := range customize {
		c(&client, &transport)
	}
	client.Transport = &transport

	if req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil); err != nil {
		return nil, err
	} else if resp, err := client.Do(req); err != nil {
		return nil, err
	} else {
		return resp, nil
	}
}
