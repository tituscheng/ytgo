// Package postprocessor implements streaming post-processing via FFmpeg.
package postprocessor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"

	"ytgo/internal/downloader"
)

// StreamConverter pipes an HTTP download directly into FFmpeg for audio
// extraction, eliminating the intermediate file.
type StreamConverter struct {
	ffmpeg string
}

// NewStreamConverter creates a StreamConverter.
func NewStreamConverter(ffmpegPath string) *StreamConverter {
	return &StreamConverter{ffmpeg: findFFmpeg(ffmpegPath)}
}

// ExtractAudio downloads url and streams it directly through FFmpeg to
// produce an audio-only output file. No intermediate file is created.
func (sc *StreamConverter) ExtractAudio(
	ctx context.Context,
	client *http.Client,
	url string,
	outputPath string,
	audioFormat string,
	quality string,
) error {
	if sc.ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found")
	}

	pr, pw := io.Pipe()

	args := []string{"-y", "-loglevel", "warning", "-i", "pipe:0"}
	args = append(args, sc.audioCodecArgs(audioFormat, quality)...)
	args = append(args, "-vn", outputPath)

	cmd := exec.CommandContext(ctx, sc.ffmpeg, args...)
	cmd.Stdin = pr

	// Start FFmpeg before we begin writing to the pipe.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	// Download into the pipe in a goroutine.
	dlErrCh := make(chan error, 1)
	go func() {
		d := &downloader.Downloader{Client: client}
		err := d.Download(ctx, url, pw)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		dlErrCh <- err
	}()

	// Wait for FFmpeg to finish.
	ffErr := cmd.Wait()
	dlErr := <-dlErrCh

	if ffErr != nil {
		return fmt.Errorf("ffmpeg convert: %w", ffErr)
	}
	if dlErr != nil {
		return fmt.Errorf("download: %w", dlErr)
	}
	return nil
}

func (sc *StreamConverter) audioCodecArgs(audioFormat, quality string) []string {
	switch audioFormat {
	case "mp3":
		args := []string{"-c:a", "libmp3lame"}
		if q, err := strconv.Atoi(quality); err == nil && q >= 0 && q <= 9 {
			args = append(args, "-q:a", quality)
		}
		return args
	case "m4a", "aac":
		return []string{"-c:a", "aac", "-b:a", "192k"}
	case "opus":
		return []string{"-c:a", "libopus"}
	case "wav":
		return []string{"-c:a", "pcm_s16le"}
	case "flac":
		return []string{"-c:a", "flac"}
	case "vorbis", "ogg":
		return []string{"-c:a", "libvorbis"}
	default:
		return []string{"-c:a", "copy"}
	}
}
