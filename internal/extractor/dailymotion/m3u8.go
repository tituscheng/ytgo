package dailymotion

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/transport"
)

var (
	streamInfPattern = regexp.MustCompile(`#EXT-X-STREAM-INF:(?P<attrs>.+)`)
	mediaPattern     = regexp.MustCompile(`#EXT-X-MEDIA:(?P<attrs>.+)`)
	audioGroupIDRe   = regexp.MustCompile(`(?i)q(\d+)`)
)

type streamInfAttrs struct {
	width     int
	height    int
	bandwidth int
	codecs    string
	frameRate float64
	name      string
	audio     string // AUDIO group id when demuxed
}

type audioMedia struct {
	groupID string
	name    string
	lang    string
	uri     string
}

type playlistResult struct {
	formats   []extractor.Format
	subtitles map[string][]extractor.Subtitle
}

// expandHLSFormats fetches master playlists for HLS formats and replaces them
// with per-variant (and demuxed audio) formats. Progressive HTTP formats are
// left unchanged. On fetch/parse failure the original HLS format is kept.
func expandHLSFormats(ctx context.Context, client *http.Client, formats []extractor.Format, duration time.Duration) []extractor.Format {
	if len(formats) == 0 {
		return formats
	}

	out := make([]extractor.Format, 0, len(formats))
	for _, f := range formats {
		if !isHLSFormat(f) {
			out = append(out, f)
			continue
		}
		expanded, err := fetchMasterFormats(ctx, client, f.URL, duration)
		if err != nil || len(expanded) == 0 {
			out = append(out, f)
			continue
		}
		out = append(out, expanded...)
	}
	return out
}

func isHLSFormat(f extractor.Format) bool {
	if strings.HasPrefix(f.FormatID, "hls-") {
		return true
	}
	return strings.Contains(strings.ToLower(f.URL), ".m3u8")
}

func fetchMasterFormats(ctx context.Context, client *http.Client, masterURL string, duration time.Duration) ([]extractor.Format, error) {
	// CDN director intermittently returns 403 (header fingerprint / rate).
	// Retry with HTTP/1.1 + yt-dlp-style randomized "blockbuster" headers.
	httpClient := masterFetchClient(client)
	const maxAttempts = 6
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(150*(1<<(attempt-1))) * time.Millisecond
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		body, err := fetchMasterBody(ctx, httpClient, masterURL, attempt)
		if err != nil {
			lastErr = err
			continue
		}
		result, err := parseMasterPlaylist(strings.NewReader(string(body)), masterURL)
		if err != nil {
			lastErr = err
			continue
		}
		applyFilesizeApprox(result.formats, duration)
		return result.formats, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("fetch HLS master: exhausted retries")
	}
	return nil, lastErr
}

func fetchMasterBody(ctx context.Context, client *http.Client, masterURL string, attempt int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, masterURL, nil)
	if err != nil {
		return nil, err
	}
	applyMasterHeaders(req, attempt)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch HLS master: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch HLS master: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// applyMasterHeaders sets browser-like headers. Later attempts add randomized
// junk headers (yt-dlp "blockbuster") and drop Cookie, which the CDN sometimes
// rejects as a fingerprint match.
func applyMasterHeaders(req *http.Request, attempt int) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", "https://www.dailymotion.com")
	req.Header.Set("Referer", "https://www.dailymotion.com/")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Cookie helps family-filter metadata; omit on retries after the first
	// couple attempts when we hit intermittent 403s.
	if attempt < 2 {
		req.Header.Set("Cookie", "ff=off")
	}
	if attempt > 0 {
		for k, v := range blockbusterHeaders() {
			req.Header.Set(k, v)
		}
	}
}

// blockbusterHeaders mirrors yt-dlp's randomized header fingerprint for DM 403s:
// https://github.com/yt-dlp/yt-dlp/issues/15526
func blockbusterHeaders() map[string]string {
	const consonants = "bcdfghjklmnpqrstvwxz"
	randLetters := func(minN, maxN int) string {
		n := minN
		if maxN > minN {
			n += rand.Intn(maxN - minN + 1)
		}
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(consonants[rand.Intn(len(consonants))])
		}
		return b.String()
	}
	n := 2 + rand.Intn(7)
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out[randLetters(8, 24)] = randLetters(16, 32)
	}
	return out
}

// masterFetchClient returns an HTTP/1.1 client. The CDN director used for
// Dailymotion HLS masters rejects many Go HTTP/2 requests with 403.
func masterFetchClient(base *http.Client) *http.Client {
	tr := transport.NewTunedTransport()
	tr.ForceAttemptHTTP2 = false
	// Empty TLSNextProto disables HTTP/2 negotiation over TLS.
	tr.TLSNextProto = map[string]func(authority string, c *tls.Conn) http.RoundTripper{}
	timeout := time.Duration(0)
	if base != nil {
		timeout = base.Timeout
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func parseMasterPlaylist(r io.Reader, masterURL string) (playlistResult, error) {
	baseURL, err := resolveBaseURL(masterURL)
	if err != nil {
		return playlistResult{}, err
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := playlistResult{
		subtitles: make(map[string][]extractor.Subtitle),
	}
	audioByGroup := make(map[string]audioMedia)
	var audioOrder []string
	var pending *streamInfAttrs
	var videoFormats []extractor.Format

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
			if audio, ok := parseAudioMedia(line, baseURL); ok {
				if _, exists := audioByGroup[audio.groupID]; !exists {
					audioByGroup[audio.groupID] = audio
					audioOrder = append(audioOrder, audio.groupID)
				}
				continue
			}
			if sub, ok := parseSubtitleMedia(line, baseURL); ok {
				result.subtitles[sub.Language] = append(result.subtitles[sub.Language], sub)
			}
		case pending != nil && !strings.HasPrefix(line, "#"):
			manifestURL := stripURLFragment(resolveRelative(baseURL, line))
			vcodec, acodec := splitCodecs(pending.codecs)
			hasDemuxedAudio := pending.audio != ""

			// Dimensions from RESOLUTION. For vertical video (e.g. 480x848)
			// height is the long edge; ranking still prefers higher ladders.
			// FormatID uses NAME (hls-480) when present for yt-dlp parity.
			f := extractor.Format{
				FormatID:     streamFormatID(*pending),
				URL:          manifestURL,
				ManifestURL:  manifestURL,
				Ext:          "mp4",
				Width:        pending.width,
				Height:       pending.height,
				FPS:          pending.frameRate,
				TBR:          float64(pending.bandwidth) / 1000,
				VideoCodec:   vcodec,
				QualityLabel: streamQualityLabel(*pending),
				HasVideo:     true,
				HasAudio:     !hasDemuxedAudio && acodec != "",
			}
			if f.Height == 0 {
				f.Height = nameAsHeight(pending.name)
			}
			if f.HasAudio {
				f.AudioCodec = acodec
			}
			videoFormats = append(videoFormats, f)
			pending = nil
		}
	}
	if err := scanner.Err(); err != nil {
		return playlistResult{}, err
	}

	if len(videoFormats) == 0 && len(audioByGroup) == 0 {
		return playlistResult{}, fmt.Errorf("no HLS variants found in master playlist")
	}

	result.formats = append(result.formats, videoFormats...)

	if len(audioByGroup) > 0 {
		// Separate audio groups ⇒ video media playlists are video-only.
		for i := range result.formats {
			if result.formats[i].HasVideo {
				result.formats[i].HasAudio = false
				result.formats[i].AudioCodec = ""
			}
		}
		seenAudio := make(map[string]bool)
		for _, groupID := range audioOrder {
			a := audioByGroup[groupID]
			id := audioFormatID(a)
			if seenAudio[id] {
				continue
			}
			seenAudio[id] = true
			result.formats = append(result.formats, extractor.Format{
				FormatID:    id,
				URL:         a.uri,
				ManifestURL: a.uri,
				// fMP4 HLS audio remuxes cleanly as mp4; "m4a" is not a valid
				// ffmpeg -f muxer name when writing *.m4a.part.
				Ext:        "mp4",
				AudioCodec: "mp4a.40.2",
				ABR:        estimateAudioABR(a.groupID),
				Language:   a.lang,
				HasVideo:   false,
				HasAudio:   true,
			})
		}
	}

	if len(result.formats) == 0 {
		return playlistResult{}, fmt.Errorf("no HLS variants found in master playlist")
	}
	return result, nil
}

// effectiveHeight picks a scoring height: prefer NAME quality tier (yt-dlp style
// "480" for 480x848 vertical), else RESOLUTION height.
func effectiveHeight(attrs streamInfAttrs) int {
	if h := nameAsHeight(attrs.name); h > 0 {
		return h
	}
	return attrs.height
}

func applyFilesizeApprox(formats []extractor.Format, duration time.Duration) {
	if duration <= 0 {
		return
	}
	secs := duration.Seconds()
	for i := range formats {
		f := &formats[i]
		var bps float64
		if f.TBR > 0 {
			bps = f.TBR * 1000
		} else if f.ABR > 0 {
			bps = f.ABR * 1000
		}
		if bps <= 0 {
			continue
		}
		f.FilesizeApprox = int64(bps * secs / 8)
	}
}

func parseStreamInf(line string) streamInfAttrs {
	m := streamInfPattern.FindStringSubmatch(line)
	if m == nil {
		return streamInfAttrs{}
	}
	attrs := streamInfAttrs{}
	raw := parseMediaAttrs(m[1])
	if v, ok := raw["BANDWIDTH"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			attrs.bandwidth = n
		}
	} else if v, ok := raw["AVERAGE-BANDWIDTH"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			attrs.bandwidth = n
		}
	}
	for key, val := range raw {
		switch key {
		case "RESOLUTION":
			if w, h, ok := parseResolution(val); ok {
				attrs.width = w
				attrs.height = h
			}
		case "CODECS":
			attrs.codecs = val
		case "FRAME-RATE":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				attrs.frameRate = f
			}
		case "NAME":
			attrs.name = val
		case "AUDIO":
			attrs.audio = val
		}
	}
	return attrs
}

func parseAudioMedia(line, baseURL string) (audioMedia, bool) {
	m := mediaPattern.FindStringSubmatch(line)
	if m == nil {
		return audioMedia{}, false
	}
	attrs := parseMediaAttrs(m[1])
	if !strings.EqualFold(attrs["TYPE"], "AUDIO") {
		return audioMedia{}, false
	}
	uri := attrs["URI"]
	if uri == "" {
		return audioMedia{}, false
	}
	groupID := attrs["GROUP-ID"]
	if groupID == "" {
		groupID = "audio"
	}
	return audioMedia{
		groupID: groupID,
		name:    attrs["NAME"],
		lang:    normalizeLang(attrs["LANGUAGE"]),
		uri:     stripURLFragment(resolveRelative(baseURL, uri)),
	}, true
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
		URL:      stripURLFragment(resolveRelative(baseURL, uri)),
		Ext:      "vtt",
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

// splitCodecs classifies CODECS members by type (order varies; DM often lists audio first).
func splitCodecs(codecs string) (video, audio string) {
	for _, part := range strings.Split(codecs, ",") {
		c := strings.TrimSpace(part)
		if c == "" {
			continue
		}
		lower := strings.ToLower(c)
		switch {
		case strings.HasPrefix(lower, "avc"), strings.HasPrefix(lower, "hvc"),
			strings.HasPrefix(lower, "hev"), strings.HasPrefix(lower, "vp0"),
			strings.HasPrefix(lower, "vp9"), strings.HasPrefix(lower, "av01"):
			if video == "" {
				video = c
			}
		case strings.HasPrefix(lower, "mp4a"), strings.HasPrefix(lower, "opus"),
			strings.HasPrefix(lower, "ac-3"), strings.HasPrefix(lower, "ec-3"),
			strings.HasPrefix(lower, "mp3"):
			if audio == "" {
				audio = c
			}
		default:
			if video == "" {
				video = c
			} else if audio == "" {
				audio = c
			}
		}
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

func streamFormatID(attrs streamInfAttrs) string {
	if attrs.name != "" {
		// yt-dlp style: hls-480, hls-380 (NAME is the quality tier).
		return "hls-" + sanitizeFormatToken(attrs.name)
	}
	if h := effectiveHeight(attrs); h > 0 {
		return fmt.Sprintf("hls-%d", h)
	}
	if attrs.bandwidth > 0 {
		return fmt.Sprintf("hls-%dk", attrs.bandwidth/1000)
	}
	return "hls"
}

func streamQualityLabel(attrs streamInfAttrs) string {
	return qualityLabel(effectiveHeight(attrs))
}

func nameAsHeight(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	name = strings.TrimSuffix(strings.ToLower(name), "p")
	n, err := strconv.Atoi(name)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func audioFormatID(a audioMedia) string {
	g := strings.ToLower(a.groupID)
	if m := audioGroupIDRe.FindStringSubmatch(g); m != nil {
		return "hls-aac-q" + m[1]
	}
	if g != "" {
		return "hls-aac-" + sanitizeFormatToken(g)
	}
	return "hls-aac"
}

func estimateAudioABR(groupID string) float64 {
	g := strings.ToLower(groupID)
	if strings.Contains(g, "q2") || strings.Contains(g, "high") {
		return 128
	}
	if strings.Contains(g, "q1") || strings.Contains(g, "low") {
		return 64
	}
	return 96
}

func sanitizeFormatToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

func resolveBaseURL(masterURL string) (string, error) {
	if idx := strings.LastIndex(masterURL, "/"); idx >= 0 {
		return masterURL[:idx+1], nil
	}
	return "", fmt.Errorf("invalid master playlist URL: %s", masterURL)
}

func resolveRelative(baseURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	return baseURL + ref
}

func stripURLFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}
