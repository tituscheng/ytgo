// Package subtitle handles subtitle downloading and format conversion.
package subtitle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/transport"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

const (
	// maxSubtitleBytes caps a single subtitle download to guard against runaway responses.
	maxSubtitleBytes = 16 << 20 // 16 MiB

	defaultMaxAttempts = 3
	defaultBackoff     = 250 * time.Millisecond
	maxBackoff         = 5 * time.Second
)

// Downloader downloads and converts subtitle tracks.
type Downloader struct {
	Client      *http.Client
	Logger      *slog.Logger
	MaxAttempts int
	BaseBackoff time.Duration
}

// NewDownloader creates a subtitle downloader. If client is nil a tuned default
// is used so subtitle fetches share the same transport conventions as the rest
// of the app.
func NewDownloader(client *http.Client) *Downloader {
	if client == nil {
		client = transport.NewTunedClient(30 * time.Second)
	}
	return &Downloader{
		Client:      client,
		MaxAttempts: defaultMaxAttempts,
		BaseBackoff: defaultBackoff,
	}
}

// transientErr is returned by fetch when the failure looks worth retrying.
// It wraps the underlying error and optionally carries a server-suggested
// retry delay.
type transientErr struct {
	err        error
	retryAfter time.Duration
}

func (e *transientErr) Error() string { return e.err.Error() }
func (e *transientErr) Unwrap() error { return e.err }

// IsRetryable reports whether an error returned from Download is transient.
// Useful for callers wanting to surface that distinction (e.g. structured
// error reports).
func IsRetryable(err error) bool {
	var te *transientErr
	return errors.As(err, &te)
}

// Download fetches a subtitle track and writes it to disk in the requested format.
// The write is atomic: data is written to destPath+".tmp" and renamed only on
// full success. The parent directory of destPath is created if missing.
func (d *Downloader) Download(ctx context.Context, sub extractor.Subtitle, destPath string, targetFormat string) error {
	if targetFormat == "" {
		targetFormat = "srt"
	}
	if err := validateTargetFormat(targetFormat); err != nil {
		return err
	}

	// Force the JSON3 timedtext format regardless of the URL YouTube handed us;
	// XML variants (srv1/srv2/srv3) aren't parsed here.
	fetchURL := forceJSON3(sub.URL)
	data, err := d.fetchWithRetry(ctx, fetchURL)
	if err != nil {
		return fmt.Errorf("fetch subtitle: %w", err)
	}

	converted, err := Convert(data, "json3", targetFormat)
	if err != nil {
		return fmt.Errorf("convert subtitle: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create subtitle dir: %w", err)
	}
	if err := writeFileAtomic(destPath, []byte(converted), 0o644); err != nil {
		return fmt.Errorf("write subtitle: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a .tmp sibling and renames into place so a
// partial file is never visible at destPath. The tmp file is removed on any
// error.
func writeFileAtomic(destPath string, data []byte, perm os.FileMode) error {
	tmp := destPath + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (d *Downloader) fetchWithRetry(ctx context.Context, fetchURL string) ([]byte, error) {
	attempts := d.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	base := d.BaseBackoff
	if base <= 0 {
		base = defaultBackoff
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := d.fetch(ctx, fetchURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		var te *transientErr
		if !errors.As(err, &te) {
			return nil, err
		}
		wait := backoffDuration(attempt, base, te.retryAfter)
		if d.Logger != nil {
			d.Logger.LogAttrs(ctx, slog.LevelDebug, "subtitle fetch retry",
				slog.Int("attempt", attempt),
				slog.Duration("sleep", wait),
				slog.String("error", err.Error()),
			)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

// backoffDuration returns the wait for the next attempt: server-suggested
// retry-after if positive, otherwise exponential backoff with jitter, capped.
func backoffDuration(attempt int, base, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > maxBackoff {
			return maxBackoff
		}
		return retryAfter
	}
	d := base << (attempt - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	// Add up to 25% jitter.
	jitter := time.Duration(rand.Int63n(int64(d) / 4))
	return d + jitter
}

func (d *Downloader) fetch(ctx context.Context, fetchURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		// url.Error covers timeouts, connection reset, etc.
		if isTransientNetErr(err) {
			return nil, &transientErr{err: err}
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a little so the connection can be reused.
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		httpErr := fmt.Errorf("HTTP %d", resp.StatusCode)
		if isTransientStatus(resp.StatusCode) {
			return nil, &transientErr{
				err:        httpErr,
				retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
		}
		return nil, httpErr
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxSubtitleBytes))
}

func isTransientStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() || urlErr.Temporary() { //nolint:staticcheck // Temporary() useful for net errors
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "temporary") ||
		strings.Contains(msg, "timeout")
}

// parseRetryAfter handles both delta-seconds and HTTP-date forms.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// forceJSON3 rewrites a YouTube timedtext URL so the response is JSON3.
// Falls back to the original URL on parse failure.
func forceJSON3(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("fmt", "json3")
	u.RawQuery = q.Encode()
	return u.String()
}

func validateTargetFormat(f string) error {
	switch f {
	case "srt", "vtt":
		return nil
	default:
		return fmt.Errorf("unsupported subtitle format %q (allowed: srt, vtt)", f)
	}
}

// Convert transforms subtitle data from one format to another.
func Convert(data []byte, from, to string) (string, error) {
	switch from {
	case "json3":
		return convertFromJSON3(data, to)
	case "srv1", "srv2", "srv3":
		return "", fmt.Errorf("subtitle source format %q is not supported (only json3)", from)
	case "", "srt", "vtt":
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown subtitle source format %q", from)
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

// WriteOptions controls a batch subtitle write.
type WriteOptions struct {
	Langs     []string
	Format    string
	BasePath  string
	BaseName  string
	WriteAuto bool

	// Client is used for HTTP fetches. If nil, a tuned default is built.
	Client *http.Client
	// Logger receives debug-level progress lines if set.
	Logger *slog.Logger
	// OnError, if set, is invoked once per failing language. The returned
	// error from WriteSubs still aggregates the same failures via errors.Join.
	OnError func(lang string, err error, retryable bool)
}

// WriteSubs downloads and writes subtitle files for the requested languages.
// Per-language failures are reported via opts.OnError (if set) and accumulated
// in the returned error. Languages with no available track are also reported.
func WriteSubs(ctx context.Context, info *ytgo.VideoInfo, opts WriteOptions) ([]string, error) {
	d := NewDownloader(opts.Client)
	d.Logger = opts.Logger

	subFormat := opts.Format
	if subFormat == "" {
		subFormat = "srt"
	}

	var written []string
	var errs []error

	report := func(lang string, err error) {
		retryable := IsRetryable(err)
		errs = append(errs, fmt.Errorf("subtitle %s: %w", lang, err))
		if opts.OnError != nil {
			opts.OnError(lang, err, retryable)
		}
	}

	for _, lang := range opts.Langs {
		path := filepath.Join(opts.BasePath, fmt.Sprintf("%s.%s.%s", opts.BaseName, lang, subFormat))

		track, isAuto, ok := selectTrack(info, lang, opts.WriteAuto)
		if !ok {
			report(lang, errors.New("no track available"))
			continue
		}

		if err := d.Download(ctx, track, path, subFormat); err != nil {
			report(lang, err)
			continue
		}
		written = append(written, path)
		if d.Logger != nil {
			d.Logger.LogAttrs(ctx, slog.LevelDebug, "subtitle written",
				slog.String("lang", lang),
				slog.Bool("auto", isAuto),
				slog.String("path", path),
			)
		}
	}
	return written, errors.Join(errs...)
}

// selectTrack picks the best subtitle track for the requested language.
// Manual tracks are preferred; auto-generated tracks are only used when
// writeAuto is true. Within a language bucket, tracks whose Name does NOT
// contain "auto"/"asr" are preferred. As a defensive fallback, region-suffixed
// language codes (e.g. "en-US") are matched when the bare code misses.
func selectTrack(info *ytgo.VideoInfo, lang string, writeAuto bool) (extractor.Subtitle, bool, bool) {
	if track, ok := pickBest(info.Subtitles, lang); ok {
		return track, false, true
	}
	if writeAuto {
		if track, ok := pickBest(info.AutoSubtitles, lang); ok {
			return track, true, true
		}
	}
	return extractor.Subtitle{}, false, false
}

func pickBest(m map[string][]extractor.Subtitle, lang string) (extractor.Subtitle, bool) {
	tracks := m[lang]
	if len(tracks) == 0 {
		// Defensive fallback: match region-suffixed entries (e.g. "en-US"
		// when caller asked for "en").
		for key, ts := range m {
			if key == lang || len(ts) == 0 {
				continue
			}
			if strings.HasPrefix(key, lang+"-") {
				tracks = ts
				break
			}
		}
	}
	if len(tracks) == 0 {
		return extractor.Subtitle{}, false
	}
	best := tracks[0]
	bestScore := trackScore(best)
	for _, t := range tracks[1:] {
		if s := trackScore(t); s > bestScore {
			best = t
			bestScore = s
		}
	}
	return best, true
}

func trackScore(t extractor.Subtitle) int {
	name := strings.ToLower(t.Name)
	if strings.Contains(name, "auto") || strings.Contains(name, "asr") || t.AutoGenerated {
		return 0
	}
	return 1
}
