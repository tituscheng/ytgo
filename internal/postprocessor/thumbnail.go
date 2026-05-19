package postprocessor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ytgo/internal/extractor"
)

// DownloadThumbnail downloads the best available thumbnail to the given path
// using the provided client (for connection reuse and context cancellation).
// If client is nil, a short-lived default client is used.
func DownloadThumbnail(ctx context.Context, client *http.Client, thumbs []extractor.Thumbnail, dest string) error {
	if len(thumbs) == 0 {
		return fmt.Errorf("no thumbnails available")
	}
	best := thumbs[0]
	for _, t := range thumbs {
		if t.Width*t.Height > best.Width*best.Height {
			best = t
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return downloadFile(ctx, client, best.URL, dest)
}

func downloadFile(ctx context.Context, client *http.Client, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
