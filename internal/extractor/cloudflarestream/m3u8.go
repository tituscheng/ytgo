package cloudflarestream

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/tituscheng/ytgo/internal/extractor"
)

var (
	streamInfPattern = regexp.MustCompile(`#EXT-X-STREAM-INF:(?P<attrs>.+)`)
	mediaPattern     = regexp.MustCompile(`#EXT-X-MEDIA:(?P<attrs>.+)`)
)

type streamInfAttrs struct {
	width     int
	height    int
	bandwidth int
	codecs    string
	frameRate float64
}

type playlistResult struct {
	formats   []extractor.Format
	subtitles map[string][]extractor.Subtitle
}

func parseMasterPlaylist(r io.Reader, masterURL string) (playlistResult, error) {
	baseURL, err := resolveBaseURL(masterURL)
	if err != nil {
		return playlistResult{}, err
	}

	scanner := bufio.NewScanner(r)
	result := playlistResult{
		subtitles: make(map[string][]extractor.Subtitle),
	}
	var pending *streamInfAttrs

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := parseStreamInf(line)
			pending = &attrs
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			if sub, ok := parseSubtitleMedia(line, baseURL); ok {
				result.subtitles[sub.Language] = append(result.subtitles[sub.Language], sub)
			}
		case pending != nil && !strings.HasPrefix(line, "#"):
			manifestURL := resolveRelative(baseURL, line)
			vcodec, acodec := splitCodecs(pending.codecs)
			result.formats = append(result.formats, extractor.Format{
				FormatID:     formatID(*pending),
				URL:          manifestURL,
				ManifestURL:  manifestURL,
				Ext:          "mp4",
				Width:        pending.width,
				Height:       pending.height,
				FPS:          pending.frameRate,
				TBR:          float64(pending.bandwidth) / 1000,
				VideoCodec:   vcodec,
				AudioCodec:   acodec,
				QualityLabel: qualityLabel(pending.height),
				HasVideo:     true,
				HasAudio:     acodec != "",
			})
			pending = nil
		}
	}

	if err := scanner.Err(); err != nil {
		return playlistResult{}, err
	}
	if len(result.formats) == 0 {
		return playlistResult{}, fmt.Errorf("no HLS variants found in master playlist")
	}
	return result, nil
}

func parseStreamInf(line string) streamInfAttrs {
	m := streamInfPattern.FindStringSubmatch(line)
	if m == nil {
		return streamInfAttrs{}
	}

	attrs := streamInfAttrs{}
	for key, val := range parseMediaAttrs(m[1]) {
		switch key {
		case "RESOLUTION":
			if w, h, ok := parseResolution(val); ok {
				attrs.width = w
				attrs.height = h
			}
		case "BANDWIDTH":
			if n, err := strconv.Atoi(val); err == nil {
				attrs.bandwidth = n
			}
		case "CODECS":
			attrs.codecs = val
		case "FRAME-RATE":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				attrs.frameRate = f
			}
		}
	}
	return attrs
}

func parseSubtitleMedia(line, baseURL string) (extractor.Subtitle, bool) {
	m := mediaPattern.FindStringSubmatch(line)
	if m == nil {
		return extractor.Subtitle{}, false
	}

	attrs := parseMediaAttrs(m[1])
	if !strings.EqualFold(attrs["TYPE"], "SUBTITLES") {
		return extractor.Subtitle{}, false
	}
	uri := attrs["URI"]
	if uri == "" {
		return extractor.Subtitle{}, false
	}

	lang := normalizeLang(attrs["LANGUAGE"])
	if lang == "" {
		lang = "und"
	}
	name := attrs["NAME"]
	if name == "" {
		name = lang
	}

	return extractor.Subtitle{
		URL:      resolveRelative(baseURL, uri),
		Ext:      subtitleExt(uri),
		Language: lang,
		Name:     name,
	}, true
}

func parseMediaAttrs(raw string) map[string]string {
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

func normalizeLang(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ""
	}
	if i := strings.IndexByte(lang, '-'); i > 0 {
		return lang[:i]
	}
	return lang
}

func subtitleExt(uri string) string {
	if strings.HasSuffix(strings.ToLower(uri), ".vtt") {
		return "vtt"
	}
	return "vtt"
}

func splitCodecs(codecs string) (video, audio string) {
	parts := strings.Split(codecs, ",")
	if len(parts) > 0 {
		video = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		audio = strings.TrimSpace(parts[1])
	}
	if audio == "" {
		audio = "aac"
	}
	return video, audio
}

func parseResolution(val string) (width, height int, ok bool) {
	parts := strings.Split(val, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(parts[0])
	h, errH := strconv.Atoi(parts[1])
	if errW != nil || errH != nil {
		return 0, 0, false
	}
	return w, h, true
}

func formatID(attrs streamInfAttrs) string {
	if attrs.height > 0 {
		return fmt.Sprintf("hls-%dp", attrs.height)
	}
	if attrs.bandwidth > 0 {
		return fmt.Sprintf("hls-%dk", attrs.bandwidth/1000)
	}
	return "hls"
}

func qualityLabel(height int) string {
	if height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dp", height)
}

func maxFormatHeight(formats []extractor.Format) int {
	max := 0
	for _, f := range formats {
		if f.Height > max {
			max = f.Height
		}
	}
	return max
}

func resolveBaseURL(masterURL string) (string, error) {
	if idx := strings.LastIndex(masterURL, "/"); idx >= 0 {
		return masterURL[:idx+1], nil
	}
	return "", fmt.Errorf("invalid master playlist URL: %s", masterURL)
}

func resolveRelative(baseURL, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	return baseURL + ref
}
