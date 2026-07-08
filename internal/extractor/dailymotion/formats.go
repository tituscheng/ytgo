package dailymotion

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tituscheng/ytgo/internal/extractor"
)

const lumberjackManifest = "application/vnd.lumberjack.manifest"

var h264DimensionsPattern = regexp.MustCompile(`/H264-(\d+)x(\d+)(?:-(60))?(?:/|\.|$)`)

func parseFormats(metadata *metadataResponse) []extractor.Format {
	var formats []extractor.Format
	for quality, entries := range metadata.Qualities {
		for _, entry := range entries {
			if entry.URL == "" || entry.Type == lumberjackManifest {
				continue
			}
			streamURL := strings.Split(entry.URL, "#")[0]
			if entry.Type == "application/x-mpegURL" || strings.Contains(strings.ToLower(streamURL), ".m3u8") {
				formats = append(formats, mapHLSFormat(quality, streamURL))
				continue
			}
			formats = append(formats, mapHTTPFormat(quality, streamURL))
		}
	}
	return formats
}

func mapHTTPFormat(quality, streamURL string) extractor.Format {
	width, height, fps := parseH264Dimensions(streamURL)
	if height == 0 {
		if h, err := strconv.Atoi(quality); err == nil {
			height = h
		}
	}
	return extractor.Format{
		FormatID:     "http-" + quality,
		URL:          streamURL,
		Ext:          "mp4",
		Width:        width,
		Height:       height,
		FPS:          fps,
		QualityLabel: qualityLabel(height),
		VideoCodec:   "avc1",
		AudioCodec:   "aac",
		HasVideo:     true,
		HasAudio:     true,
	}
}

func mapHLSFormat(quality, streamURL string) extractor.Format {
	width, height, fps := parseH264Dimensions(streamURL)
	if height == 0 {
		if h, err := strconv.Atoi(quality); err == nil {
			height = h
		}
	}
	return extractor.Format{
		FormatID:     "hls-" + quality,
		URL:          streamURL,
		ManifestURL:  streamURL,
		Ext:          "mp4",
		Width:        width,
		Height:       height,
		FPS:          fps,
		QualityLabel: qualityLabel(height),
		VideoCodec:   "avc1",
		AudioCodec:   "aac",
		HasVideo:     true,
		HasAudio:     true,
	}
}

func parseH264Dimensions(streamURL string) (width, height int, fps float64) {
	m := h264DimensionsPattern.FindStringSubmatch(streamURL)
	if m == nil {
		return 0, 0, 0
	}
	width, _ = strconv.Atoi(m[1])
	height, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		fps = 60
	}
	return width, height, fps
}

func qualityLabel(height int) string {
	if height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dp", height)
}

func parseSubtitles(metadata *metadataResponse) map[string][]extractor.Subtitle {
	if len(metadata.Subtitles.Data) == 0 {
		return nil
	}
	subs := make(map[string][]extractor.Subtitle)
	for lang, entry := range metadata.Subtitles.Data {
		for _, subURL := range entry.URLs {
			if subURL == "" {
				continue
			}
			subs[lang] = append(subs[lang], extractor.Subtitle{
				URL:      subURL,
				Ext:      "vtt",
				Language: lang,
				Name:     lang,
			})
		}
	}
	if len(subs) == 0 {
		return nil
	}
	return subs
}

func parseThumbnails(metadata *metadataResponse) []extractor.Thumbnail {
	var thumbs []extractor.Thumbnail
	for id, thumbURL := range metadata.Posters {
		if thumbURL == "" {
			continue
		}
		thumbs = append(thumbs, thumbnailFromURL(id, thumbURL))
	}
	for id, thumbURL := range metadata.Thumbnails {
		if thumbURL == "" {
			continue
		}
		thumbs = append(thumbs, thumbnailFromURL(id, thumbURL))
	}
	return thumbs
}

func thumbnailFromURL(id, thumbURL string) extractor.Thumbnail {
	height, _ := strconv.Atoi(id)
	return extractor.Thumbnail{
		URL:    thumbURL,
		Height: height,
		ID:     id,
	}
}
