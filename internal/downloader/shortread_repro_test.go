package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Short range bodies must fail the download. Previously they returned success
// while leaving zeros from preallocate in the hole; FFmpeg then failed merge
// with "moov atom not found" on the incomplete MP4.
func TestSegmentShortReadRejected(t *testing.T) {
	const size = 30 * 1024 * 1024
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i%250) + 1 // never zero
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			w.WriteHeader(http.StatusOK)
			return
		}
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Write(content)
			return
		}
		var start, end int
		fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
		if end >= size {
			end = size - 1
		}
		body := content[start : end+1]
		// Truncate responses for the second half of the file.
		if start > size/2 {
			body = body[:len(body)/2]
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+len(body)-1, size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "out.bin.part")
	sd := NewSegmentDownloader(srv.Client())
	sd.Workers = 2
	sd.ChunkSize = 5 * 1024 * 1024
	sd.MaxChunkSize = 10*1024*1024 - 1

	err := sd.DownloadToFile(context.Background(), srv.URL, dest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "short read")
}

func TestSegmentFullDownloadIntact(t *testing.T) {
	const size = 12 * 1024 * 1024
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			w.WriteHeader(http.StatusOK)
			return
		}
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Write(content)
			return
		}
		var start, end int
		fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
		if end >= size {
			end = size - 1
		}
		body := content[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "out.bin.part")
	sd := NewSegmentDownloader(srv.Client())
	sd.Workers = 2
	sd.ChunkSize = 5 * 1024 * 1024
	sd.MaxChunkSize = 10*1024*1024 - 1

	require.NoError(t, sd.DownloadToFile(context.Background(), srv.URL, dest))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, content, got)
}

func TestSegmentIgnoresRangeRejected(t *testing.T) {
	const size = 12 * 1024 * 1024
	content := make([]byte, size)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			w.WriteHeader(http.StatusOK)
			return
		}
		// Ignore Range: always 200 with full body.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "out.bin.part")
	sd := NewSegmentDownloader(srv.Client())
	sd.Workers = 2
	sd.ChunkSize = 5 * 1024 * 1024
	sd.MaxChunkSize = 10*1024*1024 - 1

	err := sd.DownloadToFile(context.Background(), srv.URL, dest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ignored Range")
}
