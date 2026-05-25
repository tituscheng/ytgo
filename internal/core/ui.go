package core

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"

	"github.com/tituscheng/ytgo/internal/extractor"
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

func printAlreadyDownloaded(title, path string) {
	name := title
	if name == "" {
		name = filepath.Base(path)
	}
	color.Green("✓ Already downloaded: %s", name)
}

func printDownloading(label string) {
	color.Cyan("↓ Downloading %s...", label)
}

func printDownloadComplete(label string) {
	color.Green("✓ %s downloaded", label)
}

func printSaved(path string) {
	color.Green("✓ Saved: %s", filepath.Base(path))
}
