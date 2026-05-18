package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadToFile(t *testing.T) {
	content := []byte("Hello, this is test content for downloader!")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	d := New()
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.txt")

	err := d.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestDownloadToFileResume(t *testing.T) {
	content := []byte("Hello, this is test content for downloader!")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			// Parse range
			var start int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &start)
			if start >= len(content) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start:])
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	d := New()
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.txt")

	// Write partial content
	partial := content[:10]
	require.NoError(t, os.WriteFile(dest, partial, 0644))

	err := d.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestDownloadToFileProgress(t *testing.T) {
	content := []byte("Hello, this is test content for downloader!")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	var lastDownloaded, lastTotal int64
	d := New()
	d.Progress = func(down, total int64) {
		lastDownloaded = down
		lastTotal = total
	}

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.txt")

	err := d.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	assert.Equal(t, int64(len(content)), lastDownloaded)
	assert.Equal(t, int64(len(content)), lastTotal)
}

func TestDownloadToWriter(t *testing.T) {
	content := []byte("Hello, this is test content for downloader!")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	var buf strings.Builder
	d := New()
	err := d.Download(context.Background(), srv.URL, &buf)
	require.NoError(t, err)
	assert.Equal(t, string(content), buf.String())
}

func TestIsResumable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{}
	assert.True(t, IsResumable(context.Background(), client, srv.URL))
}

func TestParseContentRangeTotal(t *testing.T) {
	assert.Equal(t, int64(1000), parseContentRangeTotal("bytes 0-499/1000"))
	assert.Equal(t, int64(12345), parseContentRangeTotal("bytes 100-200/12345"))
	assert.Equal(t, int64(-1), parseContentRangeTotal("invalid"))
}
