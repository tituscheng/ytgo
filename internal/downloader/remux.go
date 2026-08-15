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

	"github.com/tituscheng/ytgo/internal/ffprogress"
)

// MPEG-TS packets start with sync byte 0x47.
const mpegTSSyncByte = 0x47

// IsMPEGTSFile reports whether path begins with an MPEG-TS sync byte.
// Used after native HLS fragment concat of classic .ts segment playlists.
func IsMPEGTSFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [1]byte
	n, err := f.Read(b[:])
	if err != nil || n < 1 {
		return false
	}
	return b[0] == mpegTSSyncByte
}

// wantsMP4Container is true when the destination path (or its .part base)
// is an MP4-family extension that should not remain raw MPEG-TS.
func wantsMP4Container(destPath string) bool {
	p := destPath
	if strings.HasSuffix(strings.ToLower(p), ".part") {
		p = p[:len(p)-len(".part")]
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
	switch ext {
	case "mp4", "m4a", "m4v", "mov":
		return true
	default:
		return false
	}
}

// ShouldRemuxMPEGTS reports whether path is raw MPEG-TS that should be remuxed
// into an MP4-family container.
func ShouldRemuxMPEGTS(path string) bool {
	return wantsMP4Container(path) && IsMPEGTSFile(path)
}

// RemuxMPEGTSToMP4 stream-copies an MPEG-TS file into a real MP4 at the same
// path (atomic replace via temp file). Applies aac_adtstoasc and faststart.
// No-op (nil error) when path is not MPEG-TS or does not want an MP4 container.
// progress, when non-nil, receives (out_time_ms, duration_ms).
func RemuxMPEGTSToMP4(ctx context.Context, ffmpegPath, path string, progress ProgressFunc, duration time.Duration) error {
	if path == "" || !wantsMP4Container(path) {
		return nil
	}
	if !IsMPEGTSFile(path) {
		return nil
	}
	ffmpeg := resolveFFmpeg(ffmpegPath)
	if ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found (required to remux MPEG-TS to MP4)")
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ytgo-remux-*.mp4")
	if err != nil {
		return fmt.Errorf("create remux temp: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
	}
	if progress != nil {
		args = append(args, "-progress", "pipe:1")
	}
	args = append(args,
		"-i", ffmpegDestPath(path),
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		"-f", "mp4",
		ffmpegDestPath(tmpPath),
	)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if progress != nil {
		tot := duration.Milliseconds()
		cmd.Stdout = &ffprogress.Parser{
			OnOutTime: func(outMs int64) { progress(outMs, tot) },
			OnEnd: func() {
				if tot > 0 {
					progress(tot, tot)
				}
			},
		}
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("remux MPEG-TS to MP4: %w: %s", err, msg)
		}
		return fmt.Errorf("remux MPEG-TS to MP4: %w", err)
	}

	// Replace original (TS bytes) with remuxed MP4.
	if err := os.Rename(tmpPath, path); err != nil {
		// Cross-device rename: stream copy then remove temp.
		if cerr := copyFileReplace(tmpPath, path); cerr != nil {
			return fmt.Errorf("remux replace: %w", cerr)
		}
		_ = os.Remove(tmpPath)
	}
	return nil
}

func copyFileReplace(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func resolveFFmpeg(preferred string) string {
	if preferred != "" {
		if p, err := exec.LookPath(preferred); err == nil {
			return p
		}
		// Absolute/relative path that exists.
		if st, err := os.Stat(preferred); err == nil && !st.IsDir() {
			return preferred
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}
