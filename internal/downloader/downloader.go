// Package downloader handles HTTP media download with resume support.
package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ProgressFunc is called periodically with bytes downloaded and total size.
type ProgressFunc func(downloaded, total int64)

// Downloader downloads a single file over HTTP.
type Downloader struct {
	Client   *http.Client
	Progress ProgressFunc
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

	// Open file for append if it exists
	file, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	return d.Download(ctx, url, file)
}

// Download fetches url and writes it to the provided writer.
func (d *Downloader) Download(ctx context.Context, url string, w io.Writer) error {
	var existing int64
	if fw, ok := w.(interface{ Stat() (os.FileInfo, error) }); ok {
		if fi, err := fw.Stat(); err == nil {
			existing = fi.Size()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var total int64 = -1
	if existing > 0 && resp.StatusCode == http.StatusPartialContent {
		cr := resp.Header.Get("Content-Range")
		if cr != "" {
			total = parseContentRangeTotal(cr)
		}
	} else if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, _ := strconv.ParseInt(cl, 10, 64); n > 0 {
			total = existing + n
		}
	}

	buf := make([]byte, 32*1024)
	var downloaded int64 = existing
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			downloaded += int64(n)
			if d.Progress != nil {
				d.Progress(downloaded, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
	return nil
}

func parseContentRangeTotal(cr string) int64 {
	// bytes 1000-2000/3000
	if i := len(cr) - 1; i >= 0 {
		if j := len(cr) - 1; j >= 0 && cr[j] == '/' {
			if j+1 < len(cr) {
				if n, err := strconv.ParseInt(cr[j+1:], 10, 64); err == nil {
					return n
				}
			}
		}
	}
	// fallback: find last slash
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
