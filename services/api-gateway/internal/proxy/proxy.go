// Package proxy реализует обратный прокси для маршрутизации запросов к
// book-service и user-service.
//
// Прокси использует стандартную библиотеку net/http/httputil с добавлением
// заголовка X-Forwarded-For и корректной обработки хостов.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Proxy — обратный прокси для одного бэкенд-сервиса.
type Proxy struct {
	reverseProxy *httputil.ReverseProxy
}

// New создаёт обратный прокси для указанного URL бэкенда.
func New(target *url.URL, log *slog.Logger) *Proxy {
	log.Info("proxy configured", "target", target.String())

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	}

	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

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

// Handler возвращает http.Handler для обратного прокси.
func (p *Proxy) Handler() http.Handler {
	return p.reverseProxy
}
