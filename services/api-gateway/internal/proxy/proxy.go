// Package proxy forwards the requests of the gateway to one backend service.
//
// The forwarding is done by the reverse proxy of the standard library: a backend
// speaks the same REST surface the gateway exposes, so the request body and the
// status codes travel through without a translation layer that would have to be
// kept in sync with every contract change.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Proxy is a reverse proxy in front of one backend service.
type Proxy struct {
	reverseProxy *httputil.ReverseProxy
}

// New builds the proxy of a backend address. The address is parsed once at
// startup, so a typo in the configuration fails the process instead of the first
// request that happens to be routed here.
func New(target *url.URL, log *slog.Logger) *Proxy {
	log.Info("proxy configured", "target", target.String())

	// Connections are reused between requests: the gateway talks to two backends
	// over the network on every call, and a handshake per request would double the
	// latency it adds.
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	}

	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		// The backend sees the gateway as the host it was addressed by, which is
		// what a gRPC-gateway needs to match its routes.
		req.Host = target.Host

		// Keep the original client visible behind the proxy.
		if clientIP := req.Header.Get("X-Forwarded-For"); clientIP == "" {
			req.Header.Set("X-Forwarded-For", req.RemoteAddr)
		}
	}

	return &Proxy{
		reverseProxy: &httputil.ReverseProxy{
			Director:  director,
			Transport: transport,
		},
	}
}

// Handler returns the handler that forwards requests to the backend.
func (p *Proxy) Handler() http.Handler {
	return p.reverseProxy
}
