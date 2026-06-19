package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithHeaders_InjectsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: WithHeaders(http.DefaultTransport, map[string]string{
			"User-Agent": "TestAgent/1.0",
		}),
	}

	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, "TestAgent/1.0", gotUA)
}

func TestWithHeaders_DoesNotOverrideExisting(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: WithHeaders(http.DefaultTransport, map[string]string{
			"User-Agent": "DefaultAgent/1.0",
		}),
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "CustomAgent/2.0")

	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, "CustomAgent/2.0", gotUA)
}

func TestWithHeaders_EmptyHeadersReturnsBase(t *testing.T) {
	base := http.DefaultTransport
	assert.Same(t, base, WithHeaders(base, nil))
}
