package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// === New resume-system tests ===

func TestParseContentLengthFromURL(t *testing.T) {
	assert.Equal(t, int64(12345678), ParseContentLengthFromURL("https://example.com/video?clen=12345678&expire=123"))
	assert.Equal(t, int64(42), ParseContentLengthFromURL("https://example.com/video?foo=bar&clen=42"))
	assert.Equal(t, int64(0), ParseContentLengthFromURL("https://example.com/video?foo=bar"))
	assert.Equal(t, int64(0), ParseContentLengthFromURL(""))
	assert.Equal(t, int64(0), ParseContentLengthFromURL("https://example.com/video?clen=abc"))
}

func TestResumeStateValidate(t *testing.T) {
	rs := &ResumeState{
		VideoID:       "abc123",
		FormatID:      "251",
		ContentLength: 1000,
		FileSize:      1000,
	}

	// Exact match
	assert.True(t, rs.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "251", ContentLength: 1000}, "", 1000))

	// Mismatched VideoID
	assert.False(t, rs.Validate(DownloadIdentity{VideoID: "xyz789", FormatID: "251", ContentLength: 1000}, "", 1000))

	// Mismatched FormatID
	assert.False(t, rs.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "137", ContentLength: 1000}, "", 1000))

	// Mismatched ContentLength (both non-zero)
	assert.False(t, rs.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "251", ContentLength: 2000}, "", 1000))

	// Zero ContentLength in identity → skip check
	assert.True(t, rs.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "251", ContentLength: 0}, "", 1000))

	// Zero ContentLength in state → skip check
	rs2 := &ResumeState{VideoID: "abc123", FormatID: "251", ContentLength: 0, FileSize: 1000}
	assert.True(t, rs2.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "251", ContentLength: 9999}, "", 1000))

	// Mismatched FileSize
	assert.False(t, rs.Validate(DownloadIdentity{VideoID: "abc123", FormatID: "251", ContentLength: 1000}, "", 2000))
}

func TestSegmentDownloaderNoContinue(t *testing.T) {
	content := []byte("abcdefghijklmnopqrstuvwxyz")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			var start, end int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			if start >= len(content) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= len(content) {
				end = len(content) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start : end+1])
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.bin")

	// First download: create partial file and resume state
	sd := NewSegmentDownloader(http.DefaultClient)
	sd.ChunkSize = 5
	sd.MaxChunkSize = 5
	sd.Workers = 1
	sd.Identity = &DownloadIdentity{VideoID: "v1", FormatID: "f1"}
	err := sd.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	// Verify file exists and sidecar is gone
	require.FileExists(t, dest)
	require.NoFileExists(t, resumePath(dest))

	// Now corrupt the file, recreate sidecar, and verify --no-continue wipes them
	require.NoError(t, os.WriteFile(dest, []byte("xxx"), 0644))
	stale := &ResumeState{DestPath: dest, VideoID: "v1", FormatID: "f1", FileSize: int64(len(content)), Completed: []ByteRange{{Index: 0, StartByte: 0, EndByte: 4}}}
	require.NoError(t, stale.Save())

	sd2 := NewSegmentDownloader(http.DefaultClient)
	sd2.ChunkSize = 5
	sd2.MaxChunkSize = 5
	sd2.Workers = 1
	sd2.Identity = &DownloadIdentity{VideoID: "v1", FormatID: "f1"}
	sd2.Continue = false
	err = sd2.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	// Should be complete fresh download, not resumed
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, content, got)
	require.NoFileExists(t, resumePath(dest))
}

func TestSegmentDownloaderIdentityMismatch(t *testing.T) {
	content := []byte("abcdefghijklmnopqrstuvwxyz")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			var start, end int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			if start >= len(content) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= len(content) {
				end = len(content) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start : end+1])
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.bin")

	// Create a stale resume state with wrong FormatID
	stale := &ResumeState{
		DestPath:      dest,
		VideoID:       "v1",
		FormatID:      "OLD_FMT",
		FileSize:      int64(len(content)),
		ContentLength: 0,
		Completed:     []ByteRange{{Index: 0, StartByte: 0, EndByte: 4}},
	}
	require.NoError(t, stale.Save())

	// Download with a different FormatID — should discard stale state
	sd := NewSegmentDownloader(http.DefaultClient)
	sd.ChunkSize = 5
	sd.MaxChunkSize = 5
	sd.Workers = 1
	sd.Identity = &DownloadIdentity{VideoID: "v1", FormatID: "NEW_FMT"}
	err := sd.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// Verify the sidecar was rewritten with the new FormatID
	rs, err := LoadResumeState(dest)
	require.NoError(t, err)
	assert.Nil(t, rs) // removed on success
}

func TestSegmentDownloaderPeriodicSave(t *testing.T) {
	content := []byte("abcdefghijklmnopqrstuvwxyz")
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			var start, end int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			callCount++
			// Fail on the third segment to simulate interruption
			if callCount >= 3 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if start >= len(content) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end >= len(content) {
				end = len(content) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start : end+1])
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test2.bin")

	sd := NewSegmentDownloader(http.DefaultClient)
	sd.ChunkSize = 5
	sd.MaxChunkSize = 5
	sd.Workers = 1
	sd.Identity = &DownloadIdentity{VideoID: "v2", FormatID: "f2"}
	_ = sd.DownloadToFile(context.Background(), srv.URL, dest)

	// The sidecar should exist and have recorded the first 2 completed segments
	rs, err := LoadResumeState(dest)
	require.NoError(t, err)
	require.NotNil(t, rs)
	assert.Len(t, rs.Completed, 2)
}

func TestDownloadToFilePartNaming(t *testing.T) {
	content := []byte("Hello, this is test content for downloader!")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	partPath := filepath.Join(tmpDir, "test.txt.part")

	d := New()
	d.Identity = &DownloadIdentity{VideoID: "v1", FormatID: "f1"}
	err := d.DownloadToFile(context.Background(), srv.URL, partPath)
	require.NoError(t, err)

	// The downloader itself does not rename; that is the engine's job.
	// Here we verify the .part file exists and the sidecar is cleaned up.
	require.FileExists(t, partPath)
	require.NoFileExists(t, resumePath(partPath))

	got, err := os.ReadFile(partPath)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestStatusErrorUnwrap(t *testing.T) {
	tests := []struct {
		code   int
		sentinel error
	}{
		{403, ErrForbidden},
		{429, ErrRateLimited},
		{503, ErrTransient},
		{504, ErrTransient},
		{200, nil},
		{404, nil},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			err := &StatusError{StatusCode: tt.code}
			if tt.sentinel != nil {
				assert.ErrorIs(t, err, tt.sentinel)
			} else {
				assert.NotErrorIs(t, err, ErrForbidden)
				assert.NotErrorIs(t, err, ErrRateLimited)
				assert.NotErrorIs(t, err, ErrTransient)
			}
		})
	}
}

func TestSegmentProgressTotal(t *testing.T) {
	content := []byte("0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		var start, end int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if end >= len(content) {
			end = len(content) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[start : end+1])
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "test.bin")

	var totals []int64
	var totalsMu sync.Mutex
	sd := NewSegmentDownloader(http.DefaultClient)
	sd.Workers = 4
	sd.ChunkSize = 4
	sd.MaxChunkSize = 4
	sd.Progress = func(down, tot int64) {
		totalsMu.Lock()
		totals = append(totals, tot)
		totalsMu.Unlock()
	}

	err := sd.DownloadToFile(context.Background(), srv.URL, dest)
	require.NoError(t, err)

	require.Greater(t, len(totals), 0, "progress should have been called")
	for _, tot := range totals {
		assert.Equal(t, int64(len(content)), tot, "total should always be the full file size")
	}
}
