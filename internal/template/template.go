// Package template implements yt-dlp-style output filename templates.
package template

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"ytgo/pkg/ytgo"
)

var templateRe = regexp.MustCompile(`%\(([^)]+)\)s`)

// Parse evaluates an output template using the given VideoInfo.
func Parse(tmpl string, info *ytgo.VideoInfo, ext string) string {
	out := tmpl
	replacements := map[string]string{
		"id":             info.ID,
		"title":          sanitize(info.Title),
		"uploader":       sanitize(info.Uploader),
		"channel":        sanitize(info.Channel),
		"upload_date":    info.UploadDate,
		"ext":            ext,
		"playlist":       sanitize(info.Playlist),
		"playlist_title": sanitize(info.PlaylistTitle),
		"playlist_id":    info.PlaylistID,
	}

	// playlist_index with zero-padding
	if info.PlaylistIndex > 0 {
		replacements["playlist_index"] = fmt.Sprintf("%03d", info.PlaylistIndex)
	} else {
		replacements["playlist_index"] = ""
	}

	// Handle date formatting: %(upload_date>%Y-%m-%d)s
	out = templateRe.ReplaceAllStringFunc(out, func(match string) string {
		inner := match[2 : len(match)-2] // strip %( and )s
		if idx := strings.Index(inner, ">"); idx >= 0 {
			field := inner[:idx]
			format := inner[idx+1:]
			val, ok := replacements[field]
			if !ok {
				return match
			}
			return formatDate(val, format)
		}
		if val, ok := replacements[inner]; ok {
			return val
		}
		return match
	})

	return out
}

// sanitize removes filesystem-unsafe characters.
func sanitize(name string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
	)
	return replacer.Replace(name)
}

// formatDate performs simple date formatting on YYYYMMDD strings.
func formatDate(date, format string) string {
	if len(date) != 8 {
		return date
	}
	year := date[:4]
	month := date[4:6]
	day := date[6:8]

	out := format
	out = strings.ReplaceAll(out, "%Y", year)
	out = strings.ReplaceAll(out, "%m", month)
	out = strings.ReplaceAll(out, "%d", day)
	return out
}

// BuildPath combines the template result with optional base path.
func BuildPath(tmpl string, info *ytgo.VideoInfo, ext, basePath string) string {
	parsed := Parse(tmpl, info, ext)
	if basePath != "" {
		parsed = filepath.Join(basePath, parsed)
	}
	return parsed
}
