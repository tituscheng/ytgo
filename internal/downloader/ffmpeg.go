package downloader

import (
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

	args := []string{
		"-hide_banner",
		"-loglevel", ffmpegLogLevel(fd.Quiet),
		"-y",
		"-i", url,
		"-c", "copy",
	}
	if strings.Contains(strings.ToLower(url), ".m3u8") {
		args = append(args, "-bsf:a", "aac_adtstoasc")
	}
	if format := outputFormat(destPath); format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, destPath)

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if fd.Quiet {
		cmd.Stdout = nil
		cmd.Stderr = nil
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg stream download: %w", err)
	}
	return nil
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
