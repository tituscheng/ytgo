package postprocessor

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"ytgo/internal/extractor"
)

// DownloadThumbnail downloads the best available thumbnail to the given path.
func DownloadThumbnail(thumbs []extractor.Thumbnail, dest string) error {
	if len(thumbs) == 0 {
		return fmt.Errorf("no thumbnails available")
	}
	best := thumbs[0]
	for _, t := range thumbs {
		if t.Width*t.Height > best.Width*best.Height {
			best = t
		}
	}
	return downloadFile(best.URL, dest)
}

func downloadFile(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	resp, err := http.Get(url)
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
