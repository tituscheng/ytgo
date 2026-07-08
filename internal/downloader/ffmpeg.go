package downloader

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsStreamManifest reports whether url points at an HLS or DASH manifest.
func IsStreamManifest(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mpd")
}

// FFmpegDownloader downloads adaptive streams via ffmpeg.
type FFmpegDownloader struct {
	FFmpegPath string
	Quiet      bool
	Progress   ProgressFunc
	UserAgent  string
	Headers    map[string]string
}

// DownloadToFile writes the stream at url to destPath using ffmpeg.
func (fd *FFmpegDownloader) DownloadToFile(ctx context.Context, url, destPath string) error {
	ffmpeg := fd.ffmpegPath()
	if ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found (required for HLS/DASH downloads)")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	args := fd.buildArgs(url, destPath)

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	if fd.Quiet {
		cmd.Stdout = nil
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		if fd.Quiet && stderr.Len() > 0 {
			return fmt.Errorf("ffmpeg stream download: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("ffmpeg stream download: %w", err)
	}
	return nil
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
