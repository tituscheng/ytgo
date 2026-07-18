package core

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"

	"github.com/tituscheng/ytgo/internal/extractor"
)

// Bracketed status tags for user-facing lines (yt-dlp style).
// Only these go to the terminal in normal mode; ffmpeg/slog stay quiet.
const (
	tagDownload = "download"
	tagMerge    = "merge"
	tagInfo     = "info"
	tagError    = "error"
)

func formatStreamLabel(f extractor.Format) string {
	switch {
	case f.HasVideo && !f.HasAudio:
		if f.Height > 0 {
			return fmt.Sprintf("video (%dp)", f.Height)
		}
		if f.QualityLabel != "" {
			return f.QualityLabel
		}
		return "video"
	case f.HasAudio && !f.HasVideo:
		if f.ABR > 0 {
			return fmt.Sprintf("audio (~%.0f kbps)", f.ABR)
		}
		return "audio"
	case f.QualityLabel != "":
		return f.QualityLabel
	default:
		return fmt.Sprintf("stream %s", f.FormatID)
	}
}

func newStatusSpinner(suffix string) *spinner.Spinner {
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	s.Suffix = "  " + suffix
	return s
}

// printTagged writes one user-facing line: [tag] message
func printTagged(c *color.Color, tag, msg string) {
	if c == nil {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", tag, msg)
		return
	}
	c.Fprintf(os.Stderr, "[%s] %s\n", tag, msg)
}

func printAlreadyDownloaded(title, path string) {
	name := title
	if name == "" {
		name = filepath.Base(path)
	}
	printTagged(color.New(color.FgGreen), tagInfo, "Already downloaded: "+name)
}

func printDownloading(label string) {
	printTagged(color.New(color.FgCyan), tagDownload, "Downloading "+label+"...")
}

func printDownloadComplete(label string) {
	printTagged(color.New(color.FgGreen), tagDownload, label+" downloaded")
}

func printDownloadFailed(label string, errOrSummary any) {
	printTagged(color.New(color.FgRed), tagError, fmt.Sprintf("%s failed: %v", label, errOrSummary))
}

func printSaved(path string) {
	printTagged(color.New(color.FgGreen), tagInfo, "Saved: "+filepath.Base(path))
}

func printRetry(msg string) {
	printTagged(color.New(color.FgYellow), tagDownload, msg)
}

func printMergeStatus(msg string) {
	printTagged(color.New(color.FgCyan), tagMerge, msg)
}
