package dailymotion

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/transport"
)

// Extractor implements extractor.InfoExtractor for Dailymotion.
type Extractor struct {
	client       *http.Client
	metadataBase string // test override for metadata endpoint base URL
}

// NewExtractor creates a Dailymotion extractor.
func NewExtractor(timeout time.Duration) *Extractor {
	return &Extractor{
		client: transport.NewTunedClient(timeout),
	}
}

// Name returns the extractor identifier.
func (e *Extractor) Name() string { return "dailymotion" }

// Suitable reports whether the URL is a Dailymotion video link.
func (e *Extractor) Suitable(rawURL string) bool {
	return isSuitableURL(rawURL)
}

// Extract fetches metadata for the given Dailymotion URL.
func (e *Extractor) Extract(ctx context.Context, rawURL string) (*extractor.VideoInfo, error) {
	videoID, err := parseVideoID(rawURL)
	if err != nil {
		return nil, err
	}

	metadata, err := fetchMetadata(ctx, e.client, e.metadataBase, videoID)
	if err != nil {
		return nil, err
	}

	formats := parseFormats(metadata)
	if len(formats) == 0 {
		return nil, fmt.Errorf("no downloadable formats found for Dailymotion video %s", videoID)
	}

	return &extractor.VideoInfo{
		ID:          videoID,
		Title:       metadata.Title,
		Description: metadata.Description,
		Uploader:    metadata.Owner.Screenname,
		UploaderID:  metadata.Owner.ID,
		Channel:     metadata.Owner.Screenname,
		UploadDate:  parseUploadDate(metadata.CreatedTime),
		Duration:    time.Duration(metadata.Duration) * time.Second,
		OriginalURL: rawURL,
		WebpageURL:  fmt.Sprintf("https://www.dailymotion.com/video/%s", videoID),
		Formats:     formats,
		Subtitles:   parseSubtitles(metadata),
		Thumbnails:  parseThumbnails(metadata),
	}, nil
}
