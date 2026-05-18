// Package subtitle handles subtitle downloading and format conversion.
package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ytgo/internal/extractor"
	"ytgo/pkg/ytgo"
)

// Downloader downloads and converts subtitle tracks.
type Downloader struct {
	Client *http.Client
}

// NewDownloader creates a subtitle downloader.
func NewDownloader() *Downloader {
	return &Downloader{Client: &http.Client{}}
}

// Download fetches a subtitle track and writes it to disk in the requested format.
func (d *Downloader) Download(ctx context.Context, sub extractor.Subtitle, destPath string, targetFormat string) error {
	if targetFormat == "" {
		targetFormat = "srt"
	}

	data, err := d.fetch(ctx, sub.URL)
	if err != nil {
		return fmt.Errorf("fetch subtitle: %w", err)
	}

	converted, err := Convert(data, sub.Ext, targetFormat)
	if err != nil {
		return fmt.Errorf("convert subtitle: %w", err)
	}

	if err := os.WriteFile(destPath, []byte(converted), 0644); err != nil {
		return fmt.Errorf("write subtitle: %w", err)
	}
	return nil
}

func (d *Downloader) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Convert transforms subtitle data from one format to another.
func Convert(data []byte, from, to string) (string, error) {
	switch from {
	case "json3":
		return convertFromJSON3(data, to)
	case "srv1", "srv2", "srv3":
		return convertFromXML(data, to)
	default:
		// Assume plain text / already target format
		return string(data), nil
	}
}

type json3Seg struct {
	UTF8 string `json:"utf8"`
}

// json3Event represents a YouTube JSON3 timedtext event.
type json3Event struct {
	TStartMs    int64      `json:"tStartMs"`
	DDurationMs int64      `json:"dDurationMs,omitempty"`
	SEgs        []json3Seg `json:"segs,omitempty"`
}

type json3Root struct {
	Events []json3Event `json:"events"`
}

func convertFromJSON3(data []byte, to string) (string, error) {
	var root json3Root
	if err := json.Unmarshal(data, &root); err != nil {
		return "", err
	}

	switch to {
	case "srt":
		return toSRT(root.Events), nil
	case "vtt":
		return toVTT(root.Events), nil
	default:
		return toSRT(root.Events), nil
	}
}

func convertFromXML(data []byte, to string) (string, error) {
	// TODO: implement XML timedtext parsing if needed
	return string(data), nil
}

func toSRT(events []json3Event) string {
	var b strings.Builder
	idx := 1
	for i, ev := range events {
		text := segmentsText(ev.SEgs)
		if text == "" {
			continue
		}
		start := msToSRTTime(ev.TStartMs)
		end := msToSRTTime(ev.TStartMs + ev.DDurationMs)
		if ev.DDurationMs == 0 && i+1 < len(events) {
			end = msToSRTTime(events[i+1].TStartMs)
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", idx, start, end, text)
		idx++
	}
	return b.String()
}

func toVTT(events []json3Event) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, ev := range events {
		text := segmentsText(ev.SEgs)
		if text == "" {
			continue
		}
		start := msToVTTTime(ev.TStartMs)
		end := msToVTTTime(ev.TStartMs + ev.DDurationMs)
		if ev.DDurationMs == 0 && i+1 < len(events) {
			end = msToVTTTime(events[i+1].TStartMs)
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", start, end, text)
	}
	return b.String()
}

func segmentsText(segs []json3Seg) string {
	var parts []string
	for _, s := range segs {
		parts = append(parts, s.UTF8)
	}
	return strings.Join(parts, "")
}

func msToSRTTime(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	msRemain := (d % time.Second) / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, msRemain)
}

func msToVTTTime(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	msRemain := (d % time.Second) / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, msRemain)
}

// WriteSubs downloads and writes subtitle files for the requested languages.
func WriteSubs(ctx context.Context, info *ytgo.VideoInfo, langs []string, subFormat, basePath, baseName string, writeAuto bool) ([]string, error) {
	d := NewDownloader()
	var written []string

	for _, lang := range langs {
		// Try manual subtitles first
		if subs, ok := info.Subtitles[lang]; ok && len(subs) > 0 {
			path := filepath.Join(basePath, fmt.Sprintf("%s.%s.%s", baseName, lang, subFormat))
			if err := d.Download(ctx, subs[0], path, subFormat); err == nil {
				written = append(written, path)
				continue
			}
		}
		// Fall back to auto-generated
		if writeAuto {
			if subs, ok := info.AutoSubtitles[lang]; ok && len(subs) > 0 {
				path := filepath.Join(basePath, fmt.Sprintf("%s.%s.%s", baseName, lang, subFormat))
				if err := d.Download(ctx, subs[0], path, subFormat); err == nil {
					written = append(written, path)
				}
			}
		}
	}
	return written, nil
}
