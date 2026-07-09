package hlsfrag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadToFile_ConcatenatesInOrder(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-MAP:URI="init.mp4"
#EXTINF:1.0,
a.m4s
#EXTINF:1.0,
b.m4s
#EXTINF:1.0,
c.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("INIT"))
	})
	mux.HandleFunc("/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond) // out-of-order completion
		_, _ = w.Write([]byte("AAA"))
	})
	mux.HandleFunc("/b.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("BBB"))
	})
	mux.HandleFunc("/c.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("CCC"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	var lastDown, lastTot int64
	d := &Downloader{
		Client:  srv.Client(),
		Workers: 4,
		Progress: func(down, tot int64) {
			lastDown, lastTot = down, tot
		},
	}
	err := d.DownloadToFile(context.Background(), srv.URL+"/pl.m3u8", dest)
	require.NoError(t, err)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "INITAAABBBCCC", string(data))
	assert.Equal(t, int32(4), hits.Load())
	assert.Equal(t, int64(len(data)), lastDown)
	assert.Equal(t, int64(len(data)), lastTot)
}

func TestDownloadToFile_RejectsMaster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1000
v.m3u8
`))
	}))
	defer srv.Close()

	d := &Downloader{Client: srv.Client(), Workers: 2}
	err := d.DownloadToFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "o.mp4"))
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "master") || strings.Contains(err.Error(), "media playlist"))
}

func TestDownloadToFile_RetryTransient(t *testing.T) {
	var tries atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXTINF:1.0,
seg.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/seg.m4s", func(w http.ResponseWriter, r *http.Request) {
		n := tries.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("OKDATA"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	d := &Downloader{Client: srv.Client(), Workers: 2, MaxRetries: 5}
	require.NoError(t, d.DownloadToFile(context.Background(), srv.URL+"/pl.m3u8", dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "OKDATA", string(data))
	assert.GreaterOrEqual(t, tries.Load(), int32(3))
}
