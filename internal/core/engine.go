// Package core orchestrates extraction → format selection → download → post-processing.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"

	"ytgo/internal/archive"
	"ytgo/internal/config"
	"ytgo/internal/downloader"
	"ytgo/internal/extractor"
	"ytgo/internal/format"
	"ytgo/internal/postprocessor"
	"ytgo/internal/subtitle"
	"ytgo/internal/template"
)

// Engine runs the full download pipeline.
type Engine struct {
	Extractors []extractor.InfoExtractor
	Downloader *downloader.Downloader
	Config     config.DownloadOptions
}

// NewEngine builds an Engine with default YouTube support.
func NewEngine(cfg config.DownloadOptions) *Engine {
	return &Engine{
		Extractors: []extractor.InfoExtractor{},
		Downloader: downloader.New(),
		Config:     cfg,
	}
}

// Register adds an extractor to the engine.
func (e *Engine) Register(ext extractor.InfoExtractor) {
	e.Extractors = append(e.Extractors, ext)
}

// Run executes the pipeline for a single URL.
func (e *Engine) Run(ctx context.Context, rawURL string) error {
	// 1. Find suitable extractor
	var ext extractor.InfoExtractor
	for _, candidate := range e.Extractors {
		if candidate.Suitable(rawURL) {
			ext = candidate
			break
		}
	}
	if ext == nil {
		return fmt.Errorf("no extractor found for URL: %s", rawURL)
	}

	if e.Config.Verbose {
		color.Yellow("[%s] Extracting: %s", ext.Name(), rawURL)
	}

	// 2. Extract metadata
	info, err := ext.Extract(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	if info.IsPlaylist() {
		return e.runPlaylist(ctx, info)
	}

	return e.runVideo(ctx, info)
}

func (e *Engine) runVideo(ctx context.Context, info *extractor.VideoInfo) error {
	// --simulate or --list-formats
	if e.Config.ListFormats {
		e.printFormats(info)
		return nil
	}
	if e.Config.Simulate || e.Config.SkipDownload {
		if err := e.writeSideFiles(info); err != nil {
			return err
		}
		return e.writeSubtitles(ctx, info)
	}

	isStdout := e.Config.OutputTemplate == "-"
	if isStdout {
		e.Config.NoProgress = true
		e.Config.Quiet = true
	}

	// Check archive
	if e.Config.DownloadArchive != "" {
		arch, err := archive.Open(e.Config.DownloadArchive)
		if err != nil {
			return fmt.Errorf("open archive: %w", err)
		}
		if arch.Has(info.ID) {
			if !e.Config.Quiet {
				color.Yellow("[%s] %s: has already been recorded in archive", info.ID, info.Title)
			}
			return nil
		}
	}

	// 3. Format selection
	selected, err := format.Select(e.Config.Format, info.Formats)
	if err != nil {
		return fmt.Errorf("format selection failed: %w", err)
	}

	if e.Config.Verbose {
		color.Cyan("Selected %d format(s)", len(selected))
		for _, f := range selected {
			fmt.Fprintf(os.Stderr, "  %s: %dx%d %s/%s\n", f.FormatID, f.Width, f.Height, f.VideoCodec, f.AudioCodec)
		}
	}

	// 4. Download
	outputPath, err := e.buildOutputPath(info, selected)
	if err != nil {
		return err
	}

	var downloaded []string
	for i, f := range selected {
		partPath := outputPath
		if len(selected) > 1 || isStdout {
			ext := f.Ext
			if ext == "" {
				ext = "mp4"
			}
			partPath = fmt.Sprintf("%s.f%s.%s", strings.TrimSuffix(outputPath, filepath.Ext(outputPath)), f.FormatID, ext)
		}
		if isStdout {
			partPath = filepath.Join(os.TempDir(), filepath.Base(partPath))
		}

		if err := e.downloadFormatToFile(ctx, f, partPath, i+1, len(selected)); err != nil {
			return fmt.Errorf("download format %s failed: %w", f.FormatID, err)
		}
		downloaded = append(downloaded, partPath)
	}

	// 5. Post-processing
	finalPath := outputPath
	if len(downloaded) > 1 || e.Config.MergeOutputFormat != "" || isStdout {
		// Need merge or stdout handling
		merger := postprocessor.NewMerger(e.Config.FFmpegLocation)
		merged, err := merger.Run(ctx, downloaded, outputPath, e.Config.MergeOutputFormat)
		if err != nil {
			return fmt.Errorf("merge failed: %w", err)
		}
		finalPath = merged
		// Clean up parts
		for _, p := range downloaded {
			if p != finalPath {
				os.Remove(p)
			}
		}
	}

	if e.Config.ExtractAudio {
		conv := postprocessor.NewConverter(e.Config.FFmpegLocation)
		converted, err := conv.ExtractAudio(ctx, finalPath, e.Config.AudioFormat, e.Config.AudioQuality)
		if err != nil {
			return fmt.Errorf("audio extraction failed: %w", err)
		}
		if !e.Config.KeepVideo && finalPath != converted {
			os.Remove(finalPath)
		}
		finalPath = converted
	}

	if e.Config.EmbedMetadata || e.Config.EmbedThumbnail || e.Config.EmbedSubs || e.Config.EmbedChapters {
		embedder := postprocessor.NewEmbedder(e.Config.FFmpegLocation)
		if err := embedder.Run(ctx, finalPath, info, postprocessor.EmbedOptions{
			Metadata:  e.Config.EmbedMetadata,
			Thumbnail: e.Config.EmbedThumbnail,
			Subtitles: e.Config.EmbedSubs,
			Chapters:  e.Config.EmbedChapters,
		}); err != nil {
			return fmt.Errorf("embed failed: %w", err)
		}
	}

	// Write side files
	if err := e.writeSideFiles(info); err != nil {
		return err
	}
	if err := e.writeSubtitles(ctx, info); err != nil {
		return err
	}

	// Record in archive
	if e.Config.DownloadArchive != "" {
		arch, _ := archive.Open(e.Config.DownloadArchive)
		_ = arch.Add(info.ID)
	}

	if isStdout {
		f, err := os.Open(finalPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(os.Stdout, f)
		os.Remove(finalPath)
		return err
	}

	if !e.Config.Quiet {
		color.Green("Downloaded: %s", finalPath)
	}
	return nil
}

func (e *Engine) runPlaylist(ctx context.Context, info *extractor.VideoInfo) error {
	color.Cyan("Playlist: %s (%d entries)", info.PlaylistTitle, len(info.Entries))
	for i, entry := range info.Entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry.PlaylistIndex = i + 1
		entry.PlaylistCount = len(info.Entries)
		if err := e.runVideo(ctx, entry); err != nil {
			if !e.Config.NoWarnings {
				color.Red("Error downloading %s: %v", entry.Title, err)
			}
		}
	}
	return nil
}

func (e *Engine) downloadFormatToFile(ctx context.Context, f extractor.Format, dest string, current, total int) error {
	var s *spinner.Spinner
	if !e.Config.Quiet && !e.Config.NoProgress {
		s = spinner.New(spinner.CharSets[14], 100)
		s.Suffix = fmt.Sprintf("  Downloading format %s", f.FormatID)
		s.Start()
		defer s.Stop()
	}

	e.Downloader.Progress = func(down, total int64) {
		if s != nil && total > 0 {
			pct := float64(down) / float64(total) * 100
			s.Suffix = fmt.Sprintf("  Downloading format %s (%.1f%%)", f.FormatID, pct)
		}
	}

	return e.Downloader.DownloadToFile(ctx, f.URL, dest)
}

func (e *Engine) buildOutputPath(info *extractor.VideoInfo, selected []extractor.Format) (string, error) {
	tmpl := e.Config.OutputTemplate
	if tmpl == "" {
		tmpl = "%(title)s [%(id)s].%(ext)s"
	}

	// Determine extension
	ext := "mp4"
	if len(selected) == 1 {
		ext = selected[0].Ext
	}
	if e.Config.MergeOutputFormat != "" {
		ext = e.Config.MergeOutputFormat
	} else if e.Config.ExtractAudio {
		ext = e.Config.AudioFormat
		if ext == "best" || ext == "" {
			ext = "m4a"
		}
	}
	if ext == "" {
		ext = "mp4"
	}

	return template.BuildPath(tmpl, info, ext, e.Config.Paths), nil
}

func (e *Engine) writeSideFiles(info *extractor.VideoInfo) error {
	if e.Config.WriteInfoJSON {
		path := template.BuildPath(e.Config.OutputTemplate, info, "info.json", e.Config.Paths)
		data, _ := json.MarshalIndent(info, "", "  ")
		if err := os.WriteFile(path, data, 0644); err != nil {
			return err
		}
	}
	if e.Config.WriteDescription {
		path := template.BuildPath(e.Config.OutputTemplate, info, "description", e.Config.Paths)
		if err := os.WriteFile(path, []byte(info.Description), 0644); err != nil {
			return err
		}
	}
	if e.Config.WriteThumbnail && len(info.Thumbnails) > 0 {
		path := template.BuildPath(e.Config.OutputTemplate, info, "jpg", e.Config.Paths)
		if err := postprocessor.DownloadThumbnail(info.Thumbnails, path); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) writeSubtitles(ctx context.Context, info *extractor.VideoInfo) error {
	if !e.Config.WriteSubs && !e.Config.WriteAutoSubs && !e.Config.EmbedSubs {
		return nil
	}
	langs := e.Config.SubLangs
	if len(langs) == 0 {
		langs = []string{"en"}
	}
	baseName := template.Parse(e.Config.OutputTemplate, info, "")
	baseName = strings.TrimSuffix(baseName, filepath.Ext(baseName))
	_, err := subtitle.WriteSubs(ctx, info, langs, e.Config.SubFormat, e.Config.Paths, baseName, e.Config.WriteAutoSubs)
	return err
}

func (e *Engine) printFormats(info *extractor.VideoInfo) {
	fmt.Fprintf(os.Stderr, "[youtube] %s: Available formats\n", info.ID)
	for _, f := range info.Formats {
		codec := f.VideoCodec
		if f.AudioCodec != "" && codec != "" {
			codec += ", " + f.AudioCodec
		} else if f.AudioCodec != "" {
			codec = f.AudioCodec
		}
		fmt.Fprintf(os.Stderr, "%-5s %-10s %-6s %dx%d %s ~%s\n",
			f.FormatID, f.Ext, f.QualityLabel, f.Width, f.Height, codec, humanSize(f.Filesize))
	}
}

func humanSize(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n := n / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
