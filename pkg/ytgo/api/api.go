// Package api provides a public API for downloading YouTube videos programmatically.
package api

import (
	"context"
	"time"

	"ytgo/internal/config"
	"ytgo/internal/core"
	"ytgo/internal/extractor/youtube"
	"ytgo/pkg/ytgo"
)

// DownloadOptions is the public-facing configuration struct.
type DownloadOptions = config.DownloadOptions

// DefaultOptions returns sensible defaults.
func DefaultOptions() DownloadOptions {
	return config.DefaultOptions()
}

// Download extracts and downloads a single URL.
func Download(ctx context.Context, url string, opts DownloadOptions) error {
	engine := core.NewEngine(opts)
	engine.Register(youtube.NewExtractor(opts.SocketTimeout))
	return engine.Run(ctx, url)
}

// ExtractOnly extracts metadata without downloading.
func ExtractOnly(ctx context.Context, url string, timeout time.Duration) (*ytgo.VideoInfo, error) {
	ext := youtube.NewExtractor(timeout)
	return ext.Extract(ctx, url)
}
