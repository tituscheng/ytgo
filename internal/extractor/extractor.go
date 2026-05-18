// Package extractor defines the InfoExtractor interface.
package extractor

import (
	"context"

	"ytgo/pkg/ytgo"
)

// InfoExtractor is implemented by every site-specific extractor.
type InfoExtractor interface {
	// Name returns the extractor identifier (e.g. "youtube").
	Name() string
	// Suitable reports whether this extractor can handle the URL.
	Suitable(url string) bool
	// Extract fetches metadata for the given URL.
	Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error)
}

// Re-export types so internal packages can continue using extractor.VideoInfo, etc.
type (
	VideoInfo = ytgo.VideoInfo
	Format    = ytgo.Format
	Subtitle  = ytgo.Subtitle
	Thumbnail = ytgo.Thumbnail
	Chapter   = ytgo.Chapter
)
