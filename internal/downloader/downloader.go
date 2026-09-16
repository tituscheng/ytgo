// Package downloader handles HTTP media download with resume support.
package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	case http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
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
		Client:   &http.Client{Timeout: 0}, // no timeout; caller controls via context
		Continue: true,
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

const httpRetryAttempts = 3

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
		var data []byte
		var total int64
		var done bool
		err := retryDownload(ctx, func() error {
			var err error
			data, total, done, err = d.fetchChunk(ctx, url, offset, chunkSize)
			return err
		})
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return nil
		}
		if _, werr := w.Write(data); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}
		offset += int64(len(data))
		if d.Progress != nil {
			d.Progress(offset, total)
		}
		if done || int64(len(data)) < chunkSize {
			return nil
		}
		if total > 0 && offset >= total {
			return nil
		}
	}
}

// fetchChunk retrieves one bounded Range request into memory so a retry cannot
// duplicate bytes on a sequential io.Writer.
func (d *Downloader) fetchChunk(ctx context.Context, url string, offset, chunkSize int64) (data []byte, total int64, last bool, err error) {
	end := offset + chunkSize - 1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, -1, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))

	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, -1, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if offset > 0 {
			return nil, offset, true, nil
		}
		return nil, -1, false, &StatusError{StatusCode: resp.StatusCode, URL: url}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, -1, false, &StatusError{StatusCode: resp.StatusCode, URL: url}
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		return nil, -1, false, fmt.Errorf("server ignored Range (HTTP 200 for bytes=%d-)", offset)
	}

	total = -1
	rangeStart, rangeEnd := int64(-1), int64(-1)
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		rangeStart, rangeEnd, total = parseContentRange(cr)
	} else if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > 0 {
			total = offset + n
		}
	}

	body := io.Reader(resp.Body)
	if d.Limiter != nil {
		body = d.Limiter.ThrottleReader(ctx, resp.Body)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, body); err != nil {
		return nil, total, false, fmt.Errorf("read: %w", err)
	}
	data = buf.Bytes()
	got := int64(len(data))
	if got == 0 {
		return data, total, true, nil
	}

	want := chunkSize
	if total > 0 && offset+want > total {
		want = total - offset
	}
	if resp.StatusCode == http.StatusPartialContent && rangeEnd >= rangeStart && rangeStart >= 0 {
		want = rangeEnd - rangeStart + 1
	}

	if got < want {
		if total > 0 && offset+got >= total {
			return data, total, true, nil
		}
		if resp.StatusCode == http.StatusOK && (total <= 0 || offset+got >= total) {
			return data, total, true, nil
		}
		return nil, total, false, fmt.Errorf("short read: got %d want %d bytes (bytes=%d-%d)", got, want, offset, end)
	}
	last = total > 0 && offset+got >= total
	if resp.StatusCode == http.StatusOK {
		last = true
	}
	return data, total, last, nil
}

func retryDownload(ctx context.Context, fn func() error) error {
	var last error
	for attempt := 1; attempt <= httpRetryAttempts; attempt++ {
		last = fn()
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == httpRetryAttempts || !isRetryableDownload(last) {
			return last
		}
		delay := time.Duration(attempt) * 400 * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return last
}

func isRetryableDownload(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrTransient) {
		return true
	}
	if isTransientStreamError(err) {
		return true
	}
	return strings.Contains(err.Error(), "short read")
}

func parseContentRange(cr string) (start, end, total int64) {
	start, end, total = -1, -1, -1
	cr = strings.TrimSpace(cr)
	cr = strings.TrimPrefix(cr, "bytes")
	cr = strings.TrimSpace(cr)
	dash := strings.Index(cr, "-")
	if dash < 0 {
		return
	}
	start, _ = strconv.ParseInt(strings.TrimSpace(cr[:dash]), 10, 64)
	rest := cr[dash+1:]
	if slash := strings.LastIndex(rest, "/"); slash >= 0 {
		end, _ = strconv.ParseInt(strings.TrimSpace(rest[:slash]), 10, 64)
		tot := strings.TrimSpace(rest[slash+1:])
		if tot != "*" {
			total, _ = strconv.ParseInt(tot, 10, 64)
		}
		return
	}
	end, _ = strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	return
}

func parseContentRangeTotal(cr string) int64 {
	_, _, total := parseContentRange(cr)
	return total
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
