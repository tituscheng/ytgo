// Package api provides a public API for downloading YouTube videos programmatically.
package api

import (
	"context"
	"fmt"
	"time"

	"ytgo/internal/config"
	"ytgo/internal/core"
	"ytgo/internal/extractor/youtube"
	"ytgo/internal/format"
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

// GetStreamOptions configures a stream URL resolution.
type GetStreamOptions struct {
	URL     string
	Format  string        // format selector, default "best"
	Timeout time.Duration // default 30s
}

// StreamResult holds a resolved stream URL with full metadata.
type StreamResult struct {
	URL       string
	Format    ytgo.Format
	VideoInfo *ytgo.VideoInfo
}

// GetStreamURL extracts metadata and returns the direct stream URL for the
// best matching format, along with full format and video metadata.
//
// This is useful for players that need a temporary direct URL without
// downloading the file. The returned URL includes an expiry — re-call this
// function to get a fresh URL if the old one returns 403.
func GetStreamURL(ctx context.Context, opts GetStreamOptions) (*StreamResult, error) {
	if opts.Format == "" {
		opts.Format = "best"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	ext := youtube.NewExtractor(opts.Timeout)
	info, err := ext.Extract(ctx, opts.URL)
	if err != nil {
		return nil, fmt.Errorf("extract failed: %w", err)
	}

	formats, err := format.Select(opts.Format, info.Formats)
	if err != nil {
		return nil, fmt.Errorf("format selection: %w", err)
	}
	if len(formats) == 0 {
		return nil, fmt.Errorf("no formats matched selector: %s", opts.Format)
	}

	return &StreamResult{
		URL:       formats[0].URL,
		Format:    formats[0],
		VideoInfo: info,
	}, nil
}
