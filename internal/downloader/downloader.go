// Package downloader handles HTTP media download with resume support.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/tituscheng/ytgo/internal/limiter"
)

// defaultChunkSize is the maximum bytes requested per HTTP Range request.
// YouTube's CDN throttles unbounded Range requests (bytes=0-) and very large
// bounded ranges to ~32 KB/s, but allows smaller chunks (≤ ~10 MB) at full
// speed. We use 10 MB - 1 byte to stay safely under the threshold.
const defaultChunkSize = 10*1024*1024 - 1

// Sentinel errors for HTTP status classification.
var (
	ErrForbidden   = errors.New("access forbidden")
	ErrRateLimited = errors.New("rate limited")
	ErrTransient   = errors.New("transient error")
)

// StatusError wraps an HTTP status code for typed error inspection.
type StatusError struct {
	StatusCode int
	URL        string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// Unwrap returns a sentinel error based on the status code.
func (e *StatusError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrTransient
	}
	return nil
}

// ProgressFunc is called periodically with bytes downloaded and total size.
// When downloading with Workers > 1, 'downloaded' is the global byte count
// across all segments and 'total' is the full file size.
type ProgressFunc func(downloaded, total int64)

// Downloader downloads a single file over HTTP.
type Downloader struct {
	Client     *http.Client
	Progress   ProgressFunc
	BufferPool *sync.Pool
	Workers    int // max concurrent segments; <=1 means sequential chunked
	Limiter    *limiter.GlobalLimiter
	Identity   *DownloadIdentity // nil = no resume validation
	Continue   bool              // default true; mirrors --no-continue
}

// New creates a Downloader with sensible defaults.
func New() *Downloader {
	return &Downloader{
		Client: &http.Client{Timeout: 0}, // no timeout; caller controls via context
	}
}

// DownloadToFile fetches url and writes it to destPath. If destPath already exists
// and partial data is present, it resumes using Range headers.
func (d *Downloader) DownloadToFile(ctx context.Context, url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Always use segmented downloader with bounded chunk sizes.
	// YouTube throttles unbounded Range requests, so we never use the
	// legacy single-stream path for file downloads.
	sd := NewSegmentDownloader(d.Client)
	sd.Workers = d.Workers
	if sd.Workers <= 0 {
		sd.Workers = 1
	}
	sd.ChunkSize = defaultChunkSize
	sd.Progress = d.Progress
	sd.BufferPool = d.BufferPool
	sd.Identity = d.Identity
	sd.Continue = d.Continue
	sd.Limiter = d.Limiter
	return sd.DownloadToFile(ctx, url, destPath)
}

// Download fetches url and writes it to the provided writer.
// It downloads in sequential bounded chunks to avoid YouTube throttling.
func (d *Downloader) Download(ctx context.Context, url string, w io.Writer) error {
	var existing int64
	if fw, ok := w.(interface{ Stat() (os.FileInfo, error) }); ok {
		if fi, err := fw.Stat(); err == nil {
			existing = fi.Size()
		}
	}

	chunkSize := int64(defaultChunkSize)
	offset := existing

	for {
		end := offset + chunkSize - 1

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))

		resp, err := d.Client.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			resp.Body.Close()
			break // past EOF, nothing more to download
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return &StatusError{StatusCode: resp.StatusCode, URL: url}
		}

		// Wrap response body with rate limiter if configured
		body := resp.Body
		if d.Limiter != nil {
			body = d.Limiter.ThrottleReader(ctx, resp.Body)
		}

		var total int64 = -1
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			total = parseContentRangeTotal(cr)
		} else if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, _ := strconv.ParseInt(cl, 10, 64); n > 0 {
				total = offset + n
			}
		}

		var buf []byte
		if d.BufferPool != nil {
			buf = d.BufferPool.Get().([]byte)
		} else {
			buf = make([]byte, 32*1024)
		}

		var chunkRead int64
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					resp.Body.Close()
					if d.BufferPool != nil {
						d.BufferPool.Put(buf)
					}
					return fmt.Errorf("write: %w", werr)
				}
				offset += int64(n)
				chunkRead += int64(n)
				if d.Progress != nil {
					d.Progress(offset, total)
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				resp.Body.Close()
				if d.BufferPool != nil {
					d.BufferPool.Put(buf)
				}
				return fmt.Errorf("read: %w", err)
			}
		}

		resp.Body.Close()
		if d.BufferPool != nil {
			d.BufferPool.Put(buf)
		}

		// If we read less than a full chunk, we're done.
		if chunkRead < chunkSize {
			break
		}
	}
	return nil
}

func parseContentRangeTotal(cr string) int64 {
	// bytes 1000-2000/3000 — find the total after the last slash.
	parts := []rune(cr)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' {
			if n, err := strconv.ParseInt(string(parts[i+1:]), 10, 64); err == nil {
				return n
			}
			break
		}
	}
	return -1
}

// IsResumable checks whether the server supports Range requests.
func IsResumable(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.Header.Get("Accept-Ranges") == "bytes"
}

// WaitForRateLimit sleeps for the given duration, respecting context cancellation.
func WaitForRateLimit(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
