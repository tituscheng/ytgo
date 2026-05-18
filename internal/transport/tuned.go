// Package transport provides a shared, performance-tuned HTTP transport for ytgo.
package transport

import (
	"net"
	"net/http"
	"time"
)

// NewTunedTransport returns an http.Transport optimized for high-concurrency
// media downloading. It enables HTTP/2, generous connection pooling, and
// keeps connections alive for reuse across extractors, downloaders, and
// post-processors.
func NewTunedTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       0, // unlimited
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
	}
}

// NewTunedClient returns an http.Client using the tuned transport.
func NewTunedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewTunedTransport(),
		Timeout:   timeout,
	}
}
