package cloudflarestream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/transport"
)

// Extractor implements extractor.InfoExtractor for Cloudflare Stream.
type Extractor struct {
	client *http.Client
}

// NewExtractor creates a Cloudflare Stream extractor.
func NewExtractor(timeout time.Duration) *Extractor {
	return &Extractor{
		client: transport.NewTunedClient(timeout),
	}
}

// Name returns the extractor identifier.
func (e *Extractor) Name() string { return "cloudflarestream" }

// Suitable reports whether the URL is a Cloudflare Stream link.
func (e *Extractor) Suitable(rawURL string) bool {
	_, err := parseVideoURL(rawURL)
	return err == nil
}

// Extract fetches metadata for the given Cloudflare Stream URL.
func (e *Extractor) Extract(ctx context.Context, rawURL string) (*extractor.VideoInfo, error) {
	parsed, err := parseVideoURL(rawURL)
	if err != nil {
		return nil, err
	}

	var (
		formats   []extractor.Format
		subtitles map[string][]extractor.Subtitle
	)

	if hls, err := e.fetchHLS(ctx, parsed); err == nil {
		formats = append(formats, hls.formats...)
		subtitles = hls.subtitles
	}

	if len(streamFormats(formats)) == 0 {
		if dash, err := e.fetchDASH(ctx, parsed); err == nil {
			formats = append(formats, dash...)
		}
	}

	if direct, ok := e.probeDirectMP4(ctx, parsed, maxFormatHeight(formats)); ok {
		formats = append([]extractor.Format{direct}, formats...)
	}

	if len(formats) == 0 {
		return nil, fmt.Errorf("no downloadable formats found")
	}

	return &extractor.VideoInfo{
		ID:          parsed.displayID,
		Title:       parsed.displayID,
		OriginalURL: rawURL,
		WebpageURL:  rawURL,
		Formats:     formats,
		Subtitles:   subtitles,
		Thumbnails: []extractor.Thumbnail{
			{URL: parsed.thumbnailURL()},
		},
	}, nil
}

func (e *Extractor) fetchHLS(ctx context.Context, parsed parsedVideo) (playlistResult, error) {
	masterURL := parsed.masterManifestURL()
	body, err := e.fetch(ctx, masterURL)
	if err != nil {
		return playlistResult{}, err
	}
	defer body.Close()
	return parseMasterPlaylist(body, masterURL)
}

func (e *Extractor) fetchDASH(ctx context.Context, parsed parsedVideo) ([]extractor.Format, error) {
	dashURL := parsed.dashManifestURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, dashURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DASH manifest unavailable: HTTP %d", resp.StatusCode)
	}

	return []extractor.Format{
		{
			FormatID:     "dash",
			URL:          dashURL,
			ManifestURL:  dashURL,
			Ext:          "mp4",
			VideoCodec:   "avc1",
			AudioCodec:   "aac",
			QualityLabel: "dash",
			HasVideo:     true,
			HasAudio:     true,
		},
	}, nil
}

func (e *Extractor) probeDirectMP4(ctx context.Context, parsed parsedVideo, maxHeight int) (extractor.Format, bool) {
	directURL := parsed.directDownloadURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, directURL, nil)
	if err != nil {
		return extractor.Format{}, false
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return extractor.Format{}, false
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return extractor.Format{}, false
	}

	height := maxHeight
	if height <= 0 {
		height = 1080
	}

	var filesize int64
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			filesize = n
		}
	}

	return extractor.Format{
		FormatID:     "mp4-direct",
		URL:          directURL,
		Ext:          "mp4",
		Width:        0,
		Height:       height + 1, // outrank adaptive variants for "best"
		QualityLabel: "source",
		VideoCodec:   "avc1",
		AudioCodec:   "aac",
		Filesize:     filesize,
		HasVideo:     true,
		HasAudio:     true,
	}, true
}

func (e *Extractor) fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("fetch %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func streamFormats(formats []extractor.Format) []extractor.Format {
	var out []extractor.Format
	for _, f := range formats {
		lower := strings.ToLower(f.URL)
		if strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mpd") {
			out = append(out, f)
		}
	}
	return out
}
