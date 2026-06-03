// Package api provides a public API for downloading YouTube videos programmatically.
package api

import (
	"context"
	"fmt"
	"time"

	"github.com/tituscheng/ytgo/internal/config"
	"github.com/tituscheng/ytgo/internal/core"
	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractors"
	"github.com/tituscheng/ytgo/internal/format"
	"github.com/tituscheng/ytgo/pkg/ytgo"
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
	for _, ext := range extractors.Default(opts.SocketTimeout, opts.EnrichMetadata) {
		engine.Register(ext)
	}
	_, err := engine.Run(ctx, url)
	return err
}

// ExtractOptions configures metadata extraction without downloading.
type ExtractOptions struct {
	URL     string
	Timeout time.Duration
	Enrich  bool // if true, makes secondary API calls for additional metadata (e.g. LikeCount)
}

// Extract extracts metadata without downloading.
// Use ExtractOptions.Enrich to enable secondary API calls for richer metadata.
func Extract(ctx context.Context, opts ExtractOptions) (*ytgo.VideoInfo, error) {
	ext := findExtractor(opts.URL, opts.Timeout, opts.Enrich)
	if ext == nil {
		return nil, fmt.Errorf("no extractor found for URL: %s", opts.URL)
	}
	return ext.Extract(ctx, opts.URL)
}

// ExtractOnly extracts metadata without downloading.
// It is a convenience wrapper around Extract with Enrich disabled.
func ExtractOnly(ctx context.Context, url string, timeout time.Duration) (*ytgo.VideoInfo, error) {
	return Extract(ctx, ExtractOptions{URL: url, Timeout: timeout})
}

// GetStreamOptions configures a stream URL resolution.
type GetStreamOptions struct {
	URL              string
	Format           string        // format selector, default "best"
	Timeout          time.Duration // default 30s
	Enrich           bool          // if true, makes secondary API calls for additional metadata
	PreferVideoCodec string        // boosts score for matching video codecs
	PreferAudioCodec string        // boosts score for matching audio codecs
	PreferContainer  string        // boosts score for matching container extensions
	FormatFilter     ytgo.FormatFilter
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

	ext := findExtractor(opts.URL, opts.Timeout, opts.Enrich)
	if ext == nil {
		return nil, fmt.Errorf("no extractor found for URL: %s", opts.URL)
	}
	info, err := ext.Extract(ctx, opts.URL)
	if err != nil {
		return nil, fmt.Errorf("extract failed: %w", err)
	}

	formats, err := format.SelectWithOptions(opts.Format, info.Formats, format.SelectOptions{
		Preferences: format.Preferences{
			PreferVideoCodec: opts.PreferVideoCodec,
			PreferAudioCodec: opts.PreferAudioCodec,
			PreferContainer:  opts.PreferContainer,
		},
		FormatFilter: opts.FormatFilter,
	})
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

func findExtractor(rawURL string, timeout time.Duration, enrich bool) extractor.InfoExtractor {
	for _, ext := range extractors.Default(timeout, enrich) {
		if ext.Suitable(rawURL) {
			return ext
		}
	}
	return nil
}
