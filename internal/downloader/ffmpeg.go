package downloader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ffmpegStreamMaxAttempts is how many times DownloadToFile will try a stream
// open/download before giving up. Transient CDN 5xx (e.g. Dailymotion 504)
// often clears on a short backoff.
const ffmpegStreamMaxAttempts = 3

// IsStreamManifest reports whether url points at an HLS or DASH manifest.
func IsStreamManifest(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mpd")
}

// FFmpegDownloader downloads adaptive streams via ffmpeg.
type FFmpegDownloader struct {
	FFmpegPath string
	// Quiet suppresses ffmpeg process stdout/stderr streaming. Retry notices
	// still print when LogRetries is true (default for engine use).
	Quiet bool
	// LogRetries prints one-line retry notices even when Quiet is set.
	LogRetries bool
	Progress   ProgressFunc
	UserAgent  string
	Headers    map[string]string
	// MaxAttempts overrides ffmpegStreamMaxAttempts when > 0 (tests / tuning).
	MaxAttempts int
	// RetryBase is the unit for linear backoff between attempts (1×, 2×, …).
	// Zero means 1s. Tests may set a short duration.
	RetryBase time.Duration
}

// DownloadToFile writes the stream at url to destPath using ffmpeg.
// Transient failures (5xx, timeouts, connection reset) are retried with backoff.
func (fd *FFmpegDownloader) DownloadToFile(ctx context.Context, url, destPath string) error {
	ffmpeg := fd.ffmpegPath()
	if ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found (required for HLS/DASH downloads)")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	maxAttempts := fd.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = ffmpegStreamMaxAttempts
	}
	base := fd.RetryBase
	if base <= 0 {
		base = time.Second
	}

	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if !isTransientStreamError(last) {
				return last
			}
			// Linear backoff: 1×base, 2×base, …
			delay := time.Duration(attempt-1) * base
			if fd.LogRetries || !fd.Quiet {
				// Verbose-only; one line, no raw ffmpeg dump.
				fmt.Fprintf(os.Stderr, "[download] retry %d/%d in %s: %s\n",
					attempt-1, maxAttempts, delay, SummarizeStreamError(last))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			// Discard partial output so the next attempt starts clean.
			_ = os.Remove(destPath)
		}

		last = fd.runOnce(ctx, ffmpeg, url, destPath)
		if last == nil {
			return nil
		}
	}
	return last
}

func (fd *FFmpegDownloader) runOnce(ctx context.Context, ffmpeg, url, destPath string) error {
	args := fd.buildArgs(url, destPath)

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	// Always capture stderr so transient 5xx text is available for retry
	// decisions. When Quiet is false, also mirror to the terminal (verbose).
	var stderr bytes.Buffer
	if fd.Quiet {
		cmd.Stdout = nil
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	}

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg stream download: %w: %s", err, msg)
		}
		return fmt.Errorf("ffmpeg stream download: %w", err)
	}
	return nil
}

// isTransientStreamError reports whether a stream error is worth retrying.
// Matches CDN gateway timeouts (504), overloaded origins (502/503), rate limits,
// and common network blips seen with Dailymotion/Cloudflare HLS edges.
// Also used for native hlsfrag failures (fetch playlist: HTTP 504, header timeouts).
func isTransientStreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "429"),
		strings.Contains(msg, "502"),
		strings.Contains(msg, "503"),
		strings.Contains(msg, "504"),
		strings.Contains(msg, "5xx"),
		strings.Contains(msg, "gateway time-out"),
		strings.Contains(msg, "gateway timeout"),
		strings.Contains(msg, "server returned 5"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "tls handshake timeout"),
		strings.Contains(msg, "timeout awaiting response headers"),
		strings.Contains(msg, "temporary failure"),
		strings.Contains(msg, "network is unreachable"):
		return true
	case strings.Contains(msg, "timeout") && !strings.Contains(msg, "deadline"):
		// Generic "timeout" from HTTP layers; avoid matching context deadline.
		return true
	default:
		return false
	}
}

func (fd *FFmpegDownloader) buildArgs(url, destPath string) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", ffmpegLogLevel(fd.Quiet),
		"-y",
	}
	if len(fd.Headers) > 0 {
		args = append(args, "-headers", formatFFmpegHeaders(fd.Headers))
	}
	if fd.UserAgent != "" && fd.Headers["User-Agent"] == "" {
		args = append(args, "-user_agent", fd.UserAgent)
	}
	// HLS smart defaults (independent of -N / ConcurrentFragments):
	// reuse TCP connections and allow FFmpeg multi-connection segment fetch.
	// True N-way fragment concurrency still requires a native HLS downloader.
	// Avoid -reconnect* here: it can hang indefinitely on unreachable hosts.
	if strings.Contains(strings.ToLower(url), ".m3u8") {
		args = append(args,
			"-http_persistent", "1",
			"-http_multiple", "1",
		)
	}
	args = append(args, "-i", url, "-c", "copy")
	if strings.Contains(strings.ToLower(url), ".m3u8") {
		args = append(args, "-bsf:a", "aac_adtstoasc")
	}
	if format := outputFormat(destPath); format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, destPath)
	return args
}

func (fd *FFmpegDownloader) ffmpegPath() string {
	if fd.FFmpegPath != "" {
		if p, err := exec.LookPath(fd.FFmpegPath); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func formatFFmpegHeaders(headers map[string]string) string {
	var b strings.Builder
	for k, v := range headers {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	return b.String()
}

func ffmpegLogLevel(quiet bool) string {
	if quiet {
		return "error"
	}
	return "info"
}

func outputFormat(destPath string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(destPath), "."))
	switch ext {
	case "mp4", "mkv", "webm", "mov", "m4a", "mp3":
		return ext
	case "part":
		// Engine writes to *.part during download; infer from the base name.
		base := strings.TrimSuffix(destPath, ".part")
		return outputFormat(base)
	default:
		return ""
	}
}
