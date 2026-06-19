package transport

import "net/http"

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	return h.base.RoundTrip(req)
}

// WithHeaders returns a RoundTripper that injects headers into every request
// when the header is not already set on the outgoing request.
func WithHeaders(base http.RoundTripper, headers map[string]string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if len(headers) == 0 {
		return base
	}
	copied := make(map[string]string, len(headers))
	for k, v := range headers {
		copied[k] = v
	}
	return &headerRoundTripper{base: base, headers: copied}
}
