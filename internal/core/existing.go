package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/briandowns/spinner"

	"github.com/tituscheng/ytgo/internal/config"
	"github.com/tituscheng/ytgo/internal/extractor/youtube"
)

var (
	mediaExtensions = map[string]bool{
		"mp4": true, "webm": true, "mkv": true, "m4a": true,
		"mp3": true, "opus": true, "wav": true, "flac": true,
		"avi": true, "mov": true,
	}
	intermediateFormatRe = regexp.MustCompile(`\.f[\w-]+\.`)
)

func outputDir(cfg config.DownloadOptions) string {
	if cfg.Paths != "" {
		return cfg.Paths
	}
	return "."
}

// findExistingMedia scans dir for a completed media file containing videoID.
func findExistingMedia(dir, videoID string) (string, bool) {
	if videoID == "" {
		return "", false
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.Contains(name, videoID) {
			continue
		}
		if isExcludedSidecar(name) || isIntermediateFormat(name) {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
		if !mediaExtensions[ext] {
			continue
		}
		return filepath.Join(dir, name), true
	}
	return "", false
}

func isExcludedSidecar(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".segments") ||
		strings.HasSuffix(lower, ".info.json") ||
		strings.HasSuffix(lower, ".description") ||
		strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".webp") ||
		strings.HasSuffix(lower, ".vtt") ||
		strings.HasSuffix(lower, ".srt")
}

func isIntermediateFormat(name string) bool {
	return intermediateFormatRe.MatchString(name)
}

func (e *Engine) shouldEarlySkipExisting(rawURL string) bool {
	if !e.Config.SkipExisting || e.Config.OutputTemplate == "-" {
		return false
	}
	if e.Config.ListFormats || e.Config.Simulate || e.Config.SkipDownload {
		return false
	}
	return youtube.ExtractVideoID(rawURL) != ""
}

func (e *Engine) lookupExistingMedia(videoID string, showSpinner bool) (string, bool) {
	var s *spinner.Spinner
	if showSpinner && !e.Config.Quiet {
		s = newStatusSpinner("Checking if already downloaded...")
		s.Start()
		defer s.Stop()
	}
	return findExistingMedia(outputDir(e.Config), videoID)
}

func (e *Engine) skipIfExistingMedia(videoID, title string) bool {
	if !e.Config.SkipExisting || e.Config.OutputTemplate == "-" || videoID == "" {
		return false
	}
	path, ok := findExistingMedia(outputDir(e.Config), videoID)
	if !ok {
		return false
	}
	e.logExistingSkip(videoID, title, path, "existing media found")
	return true
}

func (e *Engine) skipIfOutputExists(outputPath, videoID, title string) bool {
	if !e.Config.SkipExisting || e.Config.OutputTemplate == "-" {
		return false
	}
	if _, err := os.Stat(outputPath); err != nil {
		return false
	}
	e.logExistingSkip(videoID, title, outputPath, "output file already exists")
	return true
}

func (e *Engine) logExistingSkip(videoID, title, path, logMsg string) {
	e.log(logMsg, slog.String("video_id", videoID), slog.String("path", path))
	if e.Config.Quiet {
		return
	}
	printAlreadyDownloaded(title, path)
}
