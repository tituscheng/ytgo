package rumble

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/transport"
)

// Extractor implements extractor.InfoExtractor for Rumble.
type Extractor struct {
	client  *http.Client
	apiBase string // test override for embedJS endpoint base URL
}

// NewExtractor creates a Rumble extractor.
func NewExtractor(timeout time.Duration) *Extractor {
	return &Extractor{
		client: transport.NewTunedClient(timeout),
	}
}

// Name returns the extractor identifier.
func (e *Extractor) Name() string { return "rumble" }

// Suitable reports whether the URL is a Rumble link.
func (e *Extractor) Suitable(rawURL string) bool {
	if _, ok := parseEmbedVideoID(rawURL); ok {
		return true
	}
	return isPageURL(rawURL)
}

// Extract fetches metadata for the given Rumble URL.
func (e *Extractor) Extract(ctx context.Context, rawURL string) (*extractor.VideoInfo, error) {
	videoID, err := resolveVideoID(ctx, e.client, rawURL)
	if err != nil {
		return nil, err
	}

	video, err := fetchEmbedJSON(ctx, e.client, e.apiBase, videoID)
	if err != nil {
		return nil, err
	}

	formats := parseFormats(video)
	if len(formats) == 0 {
		return nil, fmt.Errorf("no downloadable formats found for Rumble video %s", videoID)
	}

	webpageURL := rawURL
	if !strings.Contains(strings.ToLower(rawURL), "rumble.com/embed/") {
		webpageURL = fmt.Sprintf("https://rumble.com/embed/%s", videoID)
	}

	return &extractor.VideoInfo{
		ID:          videoID,
		Title:       video.Title,
		Uploader:    video.Author.Name,
		Channel:     video.Author.Name,
		UploadDate:  parseUploadDate(video.PubDate),
		Duration:    time.Duration(video.Duration) * time.Second,
		OriginalURL: rawURL,
		WebpageURL:  webpageURL,
		Formats:     formats,
		Subtitles:   parseSubtitles(video),
		Thumbnails:  parseThumbnails(video),
	}, nil
}
