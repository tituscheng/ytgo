// Package postprocessor implements post-download processing via FFmpeg.
package postprocessor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor"
)

// PostProcessor is implemented by every post-processor stage.
type PostProcessor interface {
	Run(ctx context.Context, input string, info *extractor.VideoInfo) (output string, err error)
}

// findFFmpeg locates the ffmpeg binary.
func findFFmpeg(preferred string) string {
	if preferred != "" {
		if _, err := exec.LookPath(preferred); err == nil {
			return preferred
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// runFFmpeg executes an ffmpeg command.
// When prefix is non-empty (used in concurrent post-processing with MaxPostProcessors > 1),
// stderr/stdout are captured and emitted with the prefix to prevent interleaving (Issue 5).
// When prefix == "", the classic live-to-stderr behavior is preserved for single-video downloads.
func runFFmpeg(ctx context.Context, ffmpeg string, prefix string, args ...string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)

	if prefix == "" {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Capture output so multiple concurrent ffmpeg invocations don't interleave on the terminal.
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	writePrefixed := func(buf *bytes.Buffer) {
		if buf.Len() == 0 {
			return
		}
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			fmt.Fprintf(os.Stderr, "%s%s\n", prefix, line)
		}
	}

	writePrefixed(&stdoutBuf)
	writePrefixed(&stderrBuf)

	return err
}

// isMP4Family reports whether the extension is an MP4/M4A/MOV container.
func isMP4Family(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "mp4", "m4a", "m4v", "mov":
		return true
	}
	return false
}

// Merger merges separate audio and video files.
type Merger struct {
	ffmpeg string
	prefix string // non-empty only when concurrent post-processing is enabled (prevents interleaving)
}

// NewMerger creates a Merger (no output prefix).
func NewMerger(ffmpegPath string) *Merger {
	return &Merger{ffmpeg: findFFmpeg(ffmpegPath)}
}

// NewMergerWithPrefix creates a Merger that prefixes all ffmpeg output lines.
// Used when MaxPostProcessors > 1 to avoid interleaved terminal output.
func NewMergerWithPrefix(ffmpegPath, prefix string) *Merger {
	return &Merger{ffmpeg: findFFmpeg(ffmpegPath), prefix: prefix}
}

// Run merges the given input files into outputPath.
func (m *Merger) Run(ctx context.Context, inputs []string, outputPath, forceExt string) (string, error) {
	if m.ffmpeg == "" {
		return "", fmt.Errorf("ffmpeg not found")
	}
	if len(inputs) < 2 {
		return inputs[0], nil
	}

	ext := filepath.Ext(outputPath)
	if forceExt != "" {
		ext = "." + forceExt
		outputPath = strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ext
	}

	args := []string{"-y", "-loglevel", "warning", "-stats"}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	args = append(args, "-c", "copy")
	// Map all streams
	for i := range inputs {
		args = append(args, "-map", fmt.Sprintf("%d:a?", i))
		args = append(args, "-map", fmt.Sprintf("%d:v?", i))
	}
	if isMP4Family(ext) || isMP4Family(forceExt) {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, outputPath)

	if err := runFFmpeg(ctx, m.ffmpeg, m.prefix, args...); err != nil {
		return "", fmt.Errorf("ffmpeg merge: %w", err)
	}
	return outputPath, nil
}

// Converter extracts or converts audio/video.
type Converter struct {
	ffmpeg string
	prefix string
}

// NewConverter creates a Converter (no output prefix).
func NewConverter(ffmpegPath string) *Converter {
	return &Converter{ffmpeg: findFFmpeg(ffmpegPath)}
}

// NewConverterWithPrefix creates a Converter that prefixes ffmpeg output.
func NewConverterWithPrefix(ffmpegPath, prefix string) *Converter {
	return &Converter{ffmpeg: findFFmpeg(ffmpegPath), prefix: prefix}
}

// ExtractAudio extracts audio to the requested format.
func (c *Converter) ExtractAudio(ctx context.Context, input, audioFormat, quality string) (string, error) {
	if c.ffmpeg == "" {
		return "", fmt.Errorf("ffmpeg not found")
	}

	var ext string
	switch audioFormat {
	case "mp3":
		ext = ".mp3"
	case "m4a", "aac":
		ext = ".m4a"
	case "opus":
		ext = ".opus"
	case "wav":
		ext = ".wav"
	case "flac":
		ext = ".flac"
	case "vorbis", "ogg":
		ext = ".ogg"
	default:
		ext = ".m4a"
	}

	output := strings.TrimSuffix(input, filepath.Ext(input)) + ext
	args := []string{"-y", "-loglevel", "warning", "-i", input}

	// Audio codec selection
	switch audioFormat {
	case "mp3":
		args = append(args, "-c:a", "libmp3lame")
		if q, err := strconv.Atoi(quality); err == nil && q >= 0 && q <= 9 {
			args = append(args, "-q:a", quality)
		}
	case "m4a", "aac":
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	case "opus":
		args = append(args, "-c:a", "libopus")
	case "wav":
		args = append(args, "-c:a", "pcm_s16le")
	case "flac":
		args = append(args, "-c:a", "flac")
	case "vorbis", "ogg":
		args = append(args, "-c:a", "libvorbis")
	default:
		args = append(args, "-c:a", "copy")
	}

	args = append(args, "-vn", output)
	if isMP4Family(ext) {
		args = append(args, "-movflags", "+faststart")
	}
	if err := runFFmpeg(ctx, c.ffmpeg, c.prefix, args...); err != nil {
		return "", fmt.Errorf("ffmpeg convert: %w", err)
	}
	return output, nil
}

// EmbedOptions controls what gets embedded.
type EmbedOptions struct {
	Metadata  bool
	Thumbnail bool
	Subtitles bool
	Chapters  bool
}

// Embedder embeds metadata, thumbnails, subtitles and chapters.
type Embedder struct {
	ffmpeg string
	client *http.Client // optional; if set, used for thumbnail downloads (shares tuned transport)
	prefix string
}

// NewEmbedder creates an Embedder using a default short-lived HTTP client for thumbnails.
func NewEmbedder(ffmpegPath string) *Embedder {
	return &Embedder{ffmpeg: findFFmpeg(ffmpegPath)}
}

// NewEmbedderWithClient creates an Embedder that reuses the provided HTTP client
// (typically one built from the engine's tuned transport) for thumbnail downloads.
func NewEmbedderWithClient(ffmpegPath string, client *http.Client) *Embedder {
	return &Embedder{
		ffmpeg: findFFmpeg(ffmpegPath),
		client: client,
	}
}

// NewEmbedderWithClientAndPrefix creates an Embedder with both a custom client and output prefix
// (used to avoid interleaving when MaxPostProcessors > 1).
func NewEmbedderWithClientAndPrefix(ffmpegPath string, client *http.Client, prefix string) *Embedder {
	return &Embedder{
		ffmpeg: findFFmpeg(ffmpegPath),
		client: client,
		prefix: prefix,
	}
}

// Run embeds the requested items into the media file.
func (e *Embedder) Run(ctx context.Context, path string, info *extractor.VideoInfo, opts EmbedOptions) error {
	if e.ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found")
	}

	// For mp4/m4a we can use atomicparsley or ffmpeg.
	// ffmpeg -i input -c copy -metadata title="..." output
	args := []string{"-y", "-loglevel", "warning", "-i", path}

	if opts.Metadata {
		if info.Title != "" {
			args = append(args, "-metadata", fmt.Sprintf("title=%s", info.Title))
		}
		if info.Uploader != "" {
			args = append(args, "-metadata", fmt.Sprintf("artist=%s", info.Uploader))
		}
		if info.Description != "" {
			args = append(args, "-metadata", fmt.Sprintf("comment=%s", info.Description))
		}
	}

	if opts.Thumbnail && len(info.Thumbnails) > 0 {
		thumbPath, err := e.downloadThumbnail(ctx, info.Thumbnails)
		if err == nil {
			defer removeFile(thumbPath)
			args = append(args, "-i", thumbPath, "-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic")
		}
	}

	// Chapters via metadata file
	if opts.Chapters && len(info.Chapters) > 0 {
		metaFile, err := writeChaptersMetadata(info.Chapters)
		if err == nil {
			defer removeFile(metaFile)
			args = append(args, "-i", metaFile, "-map_metadata", "1")
		}
	}

	args = append(args, "-c", "copy")
	if isMP4Family(filepath.Ext(path)) {
		args = append(args, "-movflags", "+faststart")
	}

	// Need a temp output
	tmpPath := path + ".tmp"
	args = append(args, tmpPath)

	if err := runFFmpeg(ctx, e.ffmpeg, e.prefix, args...); err != nil {
		removeFile(tmpPath)
		return fmt.Errorf("ffmpeg embed: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		removeFile(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (e *Embedder) downloadThumbnail(ctx context.Context, thumbs []extractor.Thumbnail) (string, error) {
	if len(thumbs) == 0 {
		return "", fmt.Errorf("no thumbnails")
	}
	// Pick the highest resolution thumbnail
	best := thumbs[0]
	for _, t := range thumbs {
		if t.Width*t.Height > best.Width*best.Height {
			best = t
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, best.URL, nil)
	if err != nil {
		return "", err
	}

	// Use the injected client (preferred — shares engine's tuned transport) or fall back
	// to a short-lived default. This unifies behavior with side-file thumbnail downloads.
	var c *http.Client
	if e.client != nil {
		c = e.client
	} else {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("unexpected content-type for thumbnail: %s", ct)
	}
	f, err := os.CreateTemp("", "thumb-*.jpg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		removeFile(path)
		return "", err
	}
	f.Close()
	return path, nil
}

func writeChaptersMetadata(chapters []extractor.Chapter) (string, error) {
	f, err := os.CreateTemp("", "chapters-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintln(f, ";FFMETADATA1")
	for _, ch := range chapters {
		fmt.Fprintln(f, "[CHAPTER]")
		fmt.Fprintln(f, "TIMEBASE=1/1000")
		fmt.Fprintf(f, "START=%d\n", ch.StartTime.Milliseconds())
		if ch.EndTime > 0 {
			fmt.Fprintf(f, "END=%d\n", ch.EndTime.Milliseconds())
		}
		fmt.Fprintf(f, "title=%s\n", ch.Title)
	}
	return f.Name(), nil
}

// removeFile is a best-effort cleanup for temporary post-processing artifacts
// (thumbnails, metadata sidecars, failed .tmp outputs). Errors are ignored
// because these are non-critical temp files (typically in /tmp).
func removeFile(path string) {
	_ = os.Remove(path)
}
