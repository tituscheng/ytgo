package extractors

import (
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractor/cloudflarestream"
	"github.com/tituscheng/ytgo/internal/extractor/youtube"
)

// Default returns the built-in extractors in priority order.
func Default(timeout time.Duration, enrich bool) []extractor.InfoExtractor {
	yt := youtube.NewExtractor(timeout)
	yt.Enrich = enrich
	return []extractor.InfoExtractor{
		cloudflarestream.NewExtractor(timeout),
		yt,
	}
}
