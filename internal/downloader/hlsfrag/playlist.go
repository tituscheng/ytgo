// Package hlsfrag implements a concurrent native HLS downloader for VOD
// media playlists (especially fMP4: EXT-X-MAP + .m4s segments).
//
// It is designed to outperform FFmpeg-based remux and default yt-dlp
// (concurrent_fragment_downloads=1) by fetching many small segments in
// parallel and concatenating them in order without per-fragment temp files.
package hlsfrag

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Fragment is one init or media segment URL in playlist order.
type Fragment struct {
	// Index is 0-based among all fragments written to the output
	// (init is 0 when present, then media 1..n).
	Index int
	URL   string
	// IsInit is true for EXT-X-MAP initialization segments.
	IsInit bool
}

// Playlist is a parsed media (not master) HLS playlist.
type Playlist struct {
	// Fragments is init (optional) followed by media segments, in write order.
	Fragments []Fragment
	// TargetDuration from #EXT-X-TARGETDURATION (seconds), if present.
	TargetDuration int
	// HasEndList is true when #EXT-X-ENDLIST is present (VOD).
	HasEndList bool
	// Encrypted is true when #EXT-X-KEY with a non-NONE method is present.
	Encrypted bool
	// IsMaster is true when the document looks like a multivariant playlist.
	IsMaster bool
}

// ParseMediaPlaylist parses an HLS media playlist. Relative URIs are resolved
// against baseURL (typically the playlist URL).
func ParseMediaPlaylist(r io.Reader, baseURL string) (*Playlist, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}

	pl := &Playlist{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var pendingMapURI string
	index := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pl.IsMaster = true
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.HasEndList = true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.TargetDuration, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			method := strings.ToUpper(attrs["METHOD"])
			if method != "" && method != "NONE" {
				pl.Encrypted = true
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			pendingMapURI = attrs["URI"]
			if pendingMapURI != "" {
				abs, err := resolve(base, pendingMapURI)
				if err != nil {
					return nil, err
				}
				pl.Fragments = append(pl.Fragments, Fragment{
					Index:  index,
					URL:    abs,
					IsInit: true,
				})
				index++
				pendingMapURI = ""
			}
		case strings.HasPrefix(line, "#"):
			// other tags ignored
		default:
			// URI line
			if pl.IsMaster {
				// Master variant URI — not a media segment for our purposes.
				continue
			}
			abs, err := resolve(base, stripFragment(line))
			if err != nil {
				return nil, err
			}
			pl.Fragments = append(pl.Fragments, Fragment{
				Index: index,
				URL:   abs,
			})
			index++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if pl.IsMaster && len(pl.Fragments) == 0 {
		return pl, fmt.Errorf("master playlist (not a media playlist)")
	}
	if len(pl.Fragments) == 0 {
		return nil, fmt.Errorf("no media segments in playlist")
	}
	return pl, nil
}

func resolve(base *url.URL, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("parse segment URL %q: %w", ref, err)
	}
	return base.ResolveReference(u).String(), nil
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

// parseAttrs parses comma-separated KEY=VALUE attributes (HLS style).
func parseAttrs(raw string) map[string]string {
	attrs := make(map[string]string)
	var key strings.Builder
	var val strings.Builder
	inQuotes := false
	pendingKey := true

	flush := func() {
		k := strings.TrimSpace(key.String())
		v := strings.TrimSpace(val.String())
		v = strings.Trim(v, `"`)
		if k != "" {
			attrs[k] = v
		}
		key.Reset()
		val.Reset()
		pendingKey = true
	}

	for _, r := range raw {
		switch r {
		case '"':
			inQuotes = !inQuotes
			if !pendingKey {
				val.WriteRune(r)
			}
		case '=':
			if pendingKey && !inQuotes {
				pendingKey = false
			} else if !pendingKey {
				val.WriteRune(r)
			}
		case ',':
			if inQuotes {
				val.WriteRune(r)
			} else {
				flush()
			}
		default:
			if pendingKey {
				key.WriteRune(r)
			} else {
				val.WriteRune(r)
			}
		}
	}
	flush()
	return attrs
}
