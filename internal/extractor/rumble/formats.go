package rumble

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tituscheng/ytgo/internal/extractor"
)

func parseFormats(video *embedResponse) []extractor.Format {
	var formats []extractor.Format

	for formatType, raw := range video.UA {
		switch formatType {
		case "tar", "timeline":
			continue
		}
		formats = append(formats, parseFormatGroup(formatType, raw, video.FPS)...)
	}

	if len(formats) == 0 {
		for formatType, raw := range video.U {
			switch formatType {
			case "tar", "timeline":
				continue
			}
			formats = append(formats, parseFormatGroup(formatType, raw, video.FPS)...)
		}
	}

	return formats
}

func parseFormatGroup(formatType string, raw json.RawMessage, fps float64) []extractor.Format {
	var byHeight map[string]streamEntry
	if err := json.Unmarshal(raw, &byHeight); err == nil && len(byHeight) > 0 {
		return parseStreamMap(formatType, byHeight, fps)
	}

	var single streamEntry
	if err := json.Unmarshal(raw, &single); err == nil && single.URL != "" {
		return []extractor.Format{mapStreamEntry(formatType, "", single, fps)}
	}
	return nil
}

func parseStreamMap(formatType string, entries map[string]streamEntry, fps float64) []extractor.Format {
	var formats []extractor.Format
	for key, entry := range entries {
		if entry.URL == "" {
			continue
		}
		formats = append(formats, mapStreamEntry(formatType, key, entry, fps))
	}
	return formats
}

func mapStreamEntry(formatType, key string, entry streamEntry, fps float64) extractor.Format {
	height := entry.Meta.H
	if height == 0 {
		if h, err := strconv.Atoi(key); err == nil {
			height = h
		}
	}

	ext := formatType
	if formatType == "hls" {
		ext = "mp4"
	}
	if strings.Contains(strings.ToLower(entry.URL), ".webm") {
		ext = "webm"
	}

	formatID := formatType
	if key != "" {
		if height > 0 {
			formatID = fmt.Sprintf("%s-%dp", formatType, height)
		} else {
			formatID = fmt.Sprintf("%s-%s", formatType, key)
		}
	}

	isHLS := formatType == "hls" || strings.Contains(strings.ToLower(entry.URL), ".m3u8")
	isAudio := formatType == "audio"

	f := extractor.Format{
		FormatID:     formatID,
		URL:          entry.URL,
		Ext:          ext,
		Width:        entry.Meta.W,
		Height:       height,
		FPS:          fps,
		TBR:          float64(entry.Meta.Bitrate),
		Filesize:     entry.Meta.Size,
		QualityLabel: qualityLabel(height),
		HasVideo:     !isAudio,
		HasAudio:     isAudio || !isHLS || formatType != "timeline",
	}
	if isHLS {
		f.ManifestURL = entry.URL
		f.VideoCodec = "avc1"
		f.AudioCodec = "aac"
	}
	if formatType == "webm" {
		f.VideoCodec = "vp9"
		f.AudioCodec = "opus"
	}
	if formatType == "mp4" {
		f.VideoCodec = "avc1"
		f.AudioCodec = "aac"
	}
	return f
}

func qualityLabel(height int) string {
	if height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dp", height)
}

type ccEntry struct {
	Path     string `json:"path"`
	Language string `json:"language"`
}

func parseSubtitles(video *embedResponse) map[string][]extractor.Subtitle {
	if len(video.CC) == 0 || string(video.CC) == "[]" || string(video.CC) == "null" {
		return nil
	}

	var byLang map[string]ccEntry
	if err := json.Unmarshal(video.CC, &byLang); err == nil && len(byLang) > 0 {
		return subtitlesFromEntries(byLang)
	}

	var entries []ccEntry
	if err := json.Unmarshal(video.CC, &entries); err != nil {
		return nil
	}
	flat := make(map[string]ccEntry, len(entries))
	for i, entry := range entries {
		flat[strconv.Itoa(i)] = entry
	}
	return subtitlesFromEntries(flat)
}

func subtitlesFromEntries(entries map[string]ccEntry) map[string][]extractor.Subtitle {
	subs := make(map[string][]extractor.Subtitle)
	for key, info := range entries {
		if info.Path == "" {
			continue
		}
		lang := info.Language
		if lang == "" {
			lang = key
		}
		if lang == "" {
			lang = "und"
		}
		name := info.Language
		if name == "" {
			name = lang
		}
		subs[lang] = append(subs[lang], extractor.Subtitle{
			URL:      info.Path,
			Ext:      "vtt",
			Language: lang,
			Name:     name,
		})
	}
	if len(subs) == 0 {
		return nil
	}
	return subs
}

func parseThumbnails(video *embedResponse) []extractor.Thumbnail {
	var thumbs []extractor.Thumbnail
	for _, t := range video.T {
		if t.I == "" {
			continue
		}
		thumbs = append(thumbs, extractor.Thumbnail{
			URL:    t.I,
			Width:  t.W,
			Height: t.H,
		})
	}
	if len(thumbs) == 0 && video.I != "" {
		thumbs = append(thumbs, extractor.Thumbnail{URL: video.I})
	}
	return thumbs
}
