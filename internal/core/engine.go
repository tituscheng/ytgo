// Package core orchestrates extraction → format selection → download → post-processing.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
	"golang.org/x/sync/errgroup"

	"ytgo/internal/archive"
	"ytgo/internal/cleanup"
	"ytgo/internal/config"
	"ytgo/internal/downloader"
	"ytgo/internal/extractor"
	"ytgo/internal/format"
	"ytgo/internal/limiter"
	"ytgo/internal/pipeline"
	"ytgo/internal/postprocessor"
	"ytgo/internal/subtitle"
	"ytgo/internal/template"
	"ytgo/internal/transport"
	"ytgo/pkg/ytgo"
)

// Engine runs the full download pipeline.
type Engine struct {
	Extractors []extractor.InfoExtractor
	Downloader *downloader.Downloader
	Config     config.DownloadOptions
	Transport  *http.Transport
}

// progressAggregate sums per-format download progress into a single callback.
type progressAggregate struct {
	mu       sync.Mutex
	byFormat map[string]int64
	totals   map[string]int64
	callback func(downloaded, total int64)
}

func newProgressAggregate(cb func(downloaded, total int64)) *progressAggregate {
	return &progressAggregate{
		byFormat: make(map[string]int64),
		totals:   make(map[string]int64),
		callback: cb,
	}
}

func (pa *progressAggregate) report(formatID string, down, tot int64) {
	pa.mu.Lock()
	pa.byFormat[formatID] = down
	pa.totals[formatID] = tot
	var sumDown, sumTot int64
	for _, v := range pa.byFormat {
		sumDown += v
	}
	for _, v := range pa.totals {
		sumTot += v
	}
	pa.mu.Unlock()
	if pa.callback != nil {
		pa.callback(sumDown, sumTot)
	}
}

// NewEngine builds an Engine with default YouTube support.
func NewEngine(cfg config.DownloadOptions) *Engine {
	tp := transport.NewTunedTransport()
	return &Engine{
		Extractors: []extractor.InfoExtractor{},
		Downloader: &downloader.Downloader{
			Client:     &http.Client{Transport: tp, Timeout: 0},
			BufferPool: &sync.Pool{New: func() any { return make([]byte, 32*1024) }},
			Workers:    cfg.ConcurrentFragments,
			Limiter:    limiter.NewGlobalLimiter(cfg.LimitRate),
		},
		Config:    cfg,
		Transport: tp,

	}
}

// Register adds an extractor to the engine.
func (e *Engine) Register(ext extractor.InfoExtractor) {
	e.Extractors = append(e.Extractors, ext)
}

// Run executes the pipeline for a single URL.
// For playlist URLs it returns a PlaylistReport summarizing the outcome.
func (e *Engine) Run(ctx context.Context, rawURL string) (*ytgo.PlaylistReport, error) {
	// 1. Find suitable extractor
	var ext extractor.InfoExtractor
	for _, candidate := range e.Extractors {
		if candidate.Suitable(rawURL) {
			ext = candidate
			break
		}
	}
	if ext == nil {
		return nil, fmt.Errorf("no extractor found for URL: %s", rawURL)
	}

	e.log("extracting", slog.String("extractor", ext.Name()), slog.String("url", rawURL))
	if e.Config.Verbose {
		color.Yellow("[%s] Extracting: %s", ext.Name(), rawURL)
	}

	// 2. Extract metadata
	info, err := ext.Extract(ctx, rawURL)
	if err != nil {
		e.reportFailure(ytgo.DownloadFailure{
			URL:       rawURL,
			Stage:     "extract",
			Error:     err.Error(),
			Retryable: isRetryable(err),
		})
		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	// Open archive once for single-video runs (playlists open their own)
	var arch *archive.Archive
	if e.Config.DownloadArchive != "" && !info.IsPlaylist() {
		arch, err = archive.Open(e.Config.DownloadArchive)
		if err != nil {
			return nil, fmt.Errorf("open archive: %w", err)
		}
	}

	if info.IsPlaylist() {
		return e.runPlaylist(ctx, info)
	}

	return nil, e.runVideo(ctx, info, arch)
}

func (e *Engine) runVideo(ctx context.Context, info *extractor.VideoInfo, arch *archive.Archive) error {
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

	task, err := e.downloadVideo(ctx, info, arch)
	if err != nil {
		return err
	}
	if task == nil {
		return nil // archived or skipped
	}
	return e.postProcessVideo(ctx, task, arch)
}

// downloadVideo handles archive check, format selection, and downloading.
// It returns a videoTask for post-processing, or nil if the video was skipped.
func (e *Engine) downloadVideo(ctx context.Context, info *extractor.VideoInfo, arch *archive.Archive) (*videoTask, error) {
	isStdout := e.Config.OutputTemplate == "-"
	if isStdout {
		e.Config.NoProgress = true
		e.Config.Quiet = true
	}

	var temps cleanup.Stack
	defer temps.Cleanup()

	// Check archive (using shared instance if available)
	if arch != nil && arch.Has(info.ID) {
		if !e.Config.Quiet {
			color.Yellow("[%s] %s: has already been recorded in archive", info.ID, info.Title)
		}
		return nil, nil
	}

	// Format selection
	selected, err := format.SelectWithOptions(e.Config.Format, info.Formats, format.SelectOptions{
		Preferences: format.Preferences{
			PreferVideoCodec: e.Config.PreferVideoCodec,
			PreferAudioCodec: e.Config.PreferAudioCodec,
			PreferContainer:  e.Config.PreferContainer,
		},
		FormatFilter: e.Config.FormatFilter,
	})
	if err != nil {
		e.reportFailure(ytgo.DownloadFailure{
			VideoID:   info.ID,
			Title:     info.Title,
			URL:       info.OriginalURL,
			Stage:     "select",
			Error:     err.Error(),
			Retryable: false,
		})
		return nil, fmt.Errorf("format selection failed: %w", err)
	}

	if e.Config.Verbose {
		color.Cyan("Selected %d format(s)", len(selected))
		for _, f := range selected {
			fmt.Fprintf(os.Stderr, "  %s: %dx%d %s/%s\n", f.FormatID, f.Width, f.Height, f.VideoCodec, f.AudioCodec)
		}
	}

	// Download
	outputPath, err := e.buildOutputPath(info, selected)
	if err != nil {
		return nil, err
	}

	// --no-overwrites: skip if final file already exists
	if e.Config.NoOverwrites && !isStdout {
		if _, err := os.Stat(outputPath); err == nil {
			if !e.Config.Quiet {
				color.Yellow("[%s] %s: file already exists, skipping", info.ID, info.Title)
			}
			return nil, nil
		}
	}

	// Streaming audio extraction: when -x is set and we have a single audio
	// format, pipe the download directly into FFmpeg instead of saving an
	// intermediate file.
	if e.Config.ExtractAudio && len(selected) == 1 && selected[0].HasAudio && !isStdout {
		sc := postprocessor.NewStreamConverter(e.Config.FFmpegLocation, e.Downloader)
		if err := sc.ExtractAudio(ctx, e.Downloader.Client, selected[0].URL, outputPath, e.Config.AudioFormat, e.Config.AudioQuality); err != nil {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				URL:       info.OriginalURL,
				FormatID:  selected[0].FormatID,
				Stage:     "convert",
				Error:     err.Error(),
				Retryable: false,
			})
			return nil, fmt.Errorf("stream extract audio failed: %w", err)
		}
		return &videoTask{info: info, outputPath: outputPath, streamed: true}, nil
	}

	downloaded := make([]string, len(selected))

	// Set up progress aggregation when the caller provides a callback and
	// there are multiple formats to download.
	var pa *progressAggregate
	if e.Config.OnProgress != nil && len(selected) > 1 {
		pa = newProgressAggregate(e.Config.OnProgress)
	}

	if len(selected) == 1 {
		partPath := outputPath + ".part"
		finalPath := outputPath
		if isStdout {
			partPath = filepath.Join(os.TempDir(), filepath.Base(finalPath)) + ".part"
			finalPath = filepath.Join(os.TempDir(), filepath.Base(finalPath))
			temps.Push(partPath)
			temps.Push(finalPath)
		}
		if err := e.downloadFormatToFile(ctx, info, selected[0], partPath, 1, 1, pa); err != nil {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				URL:       info.OriginalURL,
				FormatID:  selected[0].FormatID,
				Stage:     "download",
				Error:     err.Error(),
				Retryable: isRetryable(err),
			})
			return nil, fmt.Errorf("download format %s failed: %w", selected[0].FormatID, err)
		}
		if !isStdout {
			if err := os.Rename(partPath, finalPath); err != nil {
				return nil, fmt.Errorf("rename part file: %w", err)
			}
		} else {
			temps.Pop() // finalPath is now the active output
			temps.Pop() // partPath was renamed/consumed
		}
		downloaded[0] = finalPath
	} else {
		var g errgroup.Group
		for i, f := range selected {
			i, f := i, f
			g.Go(func() error {
				ext := f.Ext
				if ext == "" {
					ext = "mp4"
				}
				partPath := fmt.Sprintf("%s.f%s.%s.part", strings.TrimSuffix(outputPath, filepath.Ext(outputPath)), f.FormatID, ext)
				finalPath := fmt.Sprintf("%s.f%s.%s", strings.TrimSuffix(outputPath, filepath.Ext(outputPath)), f.FormatID, ext)
				if isStdout {
					partPath = filepath.Join(os.TempDir(), filepath.Base(finalPath)) + ".part"
					finalPath = filepath.Join(os.TempDir(), filepath.Base(finalPath))
					temps.Push(partPath)
					temps.Push(finalPath)
				}
				if err := e.downloadFormatToFile(ctx, info, f, partPath, i+1, len(selected), pa); err != nil {
					e.reportFailure(ytgo.DownloadFailure{
						VideoID:   info.ID,
						Title:     info.Title,
						URL:       info.OriginalURL,
						FormatID:  f.FormatID,
						Stage:     "download",
						Error:     err.Error(),
						Retryable: isRetryable(err),
					})
					return fmt.Errorf("download format %s failed: %w", f.FormatID, err)
				}
				if !isStdout {
					if err := os.Rename(partPath, finalPath); err != nil {
						return fmt.Errorf("rename part file: %w", err)
					}
				} else {
					temps.Pop()
					temps.Pop()
				}
				downloaded[i] = finalPath
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	return &videoTask{info: info, outputPath: outputPath, downloaded: downloaded}, nil
}

// postProcessVideo handles merge, convert, embed, side files, and archive.
func (e *Engine) postProcessVideo(ctx context.Context, task *videoTask, arch *archive.Archive) error {
	info := task.info
	outputPath := task.outputPath
	downloaded := task.downloaded
	isStdout := e.Config.OutputTemplate == "-"

	finalPath := outputPath
	if len(downloaded) > 1 || e.Config.MergeOutputFormat != "" || isStdout {
		merger := postprocessor.NewMerger(e.Config.FFmpegLocation)
		merged, err := merger.Run(ctx, downloaded, outputPath, e.Config.MergeOutputFormat)
		if err != nil {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				Stage:     "merge",
				Error:     err.Error(),
				Retryable: false,
			})
			return fmt.Errorf("merge failed: %w", err)
		}
		finalPath = merged
		for _, p := range downloaded {
			if p != finalPath {
				os.Remove(p)
			}
		}
	}

	if e.Config.ExtractAudio && !task.streamed {
		conv := postprocessor.NewConverter(e.Config.FFmpegLocation)
		converted, err := conv.ExtractAudio(ctx, finalPath, e.Config.AudioFormat, e.Config.AudioQuality)
		if err != nil {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				Stage:     "convert",
				Error:     err.Error(),
				Retryable: false,
			})
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
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				Stage:     "embed",
				Error:     err.Error(),
				Retryable: false,
			})
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
	if arch != nil {
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

// videoTask carries state from the download stage to the post-process stage.
type videoTask struct {
	info       *extractor.VideoInfo
	outputPath string
	downloaded []string
	streamed   bool // true if audio was extracted via streaming (no intermediate file)
}

func (e *Engine) runPlaylist(ctx context.Context, info *extractor.VideoInfo) (*ytgo.PlaylistReport, error) {
	color.Cyan("Playlist: %s (%d entries)", info.PlaylistTitle, len(info.Entries))

	// Apply playlist range filters
	start := e.Config.PlaylistStart - 1
	if start < 0 {
		start = 0
	}
	end := len(info.Entries)
	if e.Config.PlaylistEnd > 0 && e.Config.PlaylistEnd < end {
		end = e.Config.PlaylistEnd
	}
	entries := info.Entries[start:end]

	report := &ytgo.PlaylistReport{
		Total: len(entries),
	}
	var reportMu sync.Mutex

	// Open archive once and share across workers
	var arch *archive.Archive
	if e.Config.DownloadArchive != "" {
		var err error
		arch, err = archive.Open(e.Config.DownloadArchive)
		if err != nil {
			return report, fmt.Errorf("open archive: %w", err)
		}
	}

	// Channel buffers tasks between download and post-process stages.
	// Capacity = MaxPostProcessors*2 provides backpressure without blocking
	// download workers for long.
	postprocChan := make(chan *videoTask, max(1, e.Config.MaxPostProcessors)*2)

	// Post-process worker pool
	postprocPool := pipeline.NewWorkerPool(e.Config.MaxPostProcessors)
	postprocPool.Start(ctx)
	for i := 0; i < max(1, e.Config.MaxPostProcessors); i++ {
		postprocPool.Submit(ctx, func() error {
			for task := range postprocChan {
				if err := e.postProcessVideo(ctx, task, arch); err != nil {
					reportMu.Lock()
					report.Failed = append(report.Failed, ytgo.DownloadFailure{
						VideoID:   task.info.ID,
						Title:     task.info.Title,
						URL:       task.info.OriginalURL,
						Stage:     "postprocess",
						Error:     err.Error(),
						Retryable: false,
					})
					reportMu.Unlock()
					if !e.Config.NoWarnings {
						color.Red("Error post-processing %s: %v", task.info.Title, err)
					}
				} else {
					reportMu.Lock()
					report.Succeeded++
					reportMu.Unlock()
				}
			}
			return nil
		})
	}

	// Download worker pool
	downloadPool := pipeline.NewWorkerPool(e.Config.MaxDownloads)
	downloadPool.Start(ctx)

	for i, entry := range entries {
		entry := entry // capture for closure
		idx := start + i

		if err := downloadPool.Submit(ctx, func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			job := *entry
			job.PlaylistIndex = idx + 1
			job.PlaylistCount = len(info.Entries)
			job.Playlist = entry.Playlist
			job.PlaylistID = entry.PlaylistID
			job.PlaylistTitle = entry.PlaylistTitle

			// If entry has no formats, re-extract it individually
			if len(job.Formats) == 0 && !e.Config.SkipDownload && !e.Config.Simulate {
				for _, ext := range e.Extractors {
					if ext.Suitable(job.OriginalURL) {
						full, err := ext.Extract(ctx, job.OriginalURL)
						if err == nil {
							full.PlaylistIndex = job.PlaylistIndex
							full.PlaylistCount = job.PlaylistCount
							full.Playlist = job.Playlist
							full.PlaylistID = job.PlaylistID
							full.PlaylistTitle = job.PlaylistTitle
							job = *full
						}
						break
					}
				}
			}

			task, err := e.downloadVideo(ctx, &job, arch)
			if err != nil {
				reportMu.Lock()
				report.Failed = append(report.Failed, ytgo.DownloadFailure{
					VideoID:   job.ID,
					Title:     job.Title,
					URL:       job.OriginalURL,
					Stage:     "download",
					Error:     err.Error(),
					Retryable: isRetryable(err),
				})
				reportMu.Unlock()
				if !e.Config.NoWarnings {
					color.Red("Error downloading %s: %v", job.Title, err)
				}
				return nil
			}
			if task == nil {
				reportMu.Lock()
				report.Skipped++
				reportMu.Unlock()
				return nil
			}
			select {
			case postprocChan <- task:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}); err != nil {
			// Context cancelled
			break
		}
	}

	// Wait for all downloads to finish, then close the post-proc channel
	downloadErr := downloadPool.Wait()
	close(postprocChan)
	postprocErr := postprocPool.Wait()

	if downloadErr != nil {
		return report, downloadErr
	}
	return report, postprocErr
}

func (e *Engine) downloadFormatToFile(ctx context.Context, info *extractor.VideoInfo, f extractor.Format, dest string, current, total int, pa *progressAggregate) error {
	// For concurrent downloads (total > 1), skip the interactive spinner to
	// avoid stderr interleaving. Instead, log start and completion.
	concurrent := total > 1
	var s *spinner.Spinner
	if !concurrent && !e.Config.Quiet && !e.Config.NoProgress {
		s = spinner.New(spinner.CharSets[14], 100)
		s.Suffix = fmt.Sprintf("  Downloading format %s", f.FormatID)
		s.Start()
		defer s.Stop()
	} else if concurrent && !e.Config.Quiet {
		color.Yellow("[start] format %s → %s", f.FormatID, dest)
		defer func() {
			color.Green("[done]  format %s → %s", f.FormatID, dest)
		}()
	}

	clen := downloader.ParseContentLengthFromURL(f.URL)

	// Build the progress callback: user callback + spinner + aggregation
	progressCb := func(down, tot int64) {
		if e.Config.OnProgress != nil && pa == nil {
			// Single-format download: report directly
			e.Config.OnProgress(down, tot)
		}
		if pa != nil {
			// Multi-format download: aggregate before reporting
			pa.report(f.FormatID, down, tot)
		}
		if s != nil && tot > 0 {
			pct := float64(down) / float64(tot) * 100
			s.Suffix = fmt.Sprintf("  Downloading format %s (%.1f%%)", f.FormatID, pct)
		}
	}

	// Use a local Downloader instance to avoid race on the Progress callback
	d := &downloader.Downloader{
		Client:     e.Downloader.Client,
		BufferPool: e.Downloader.BufferPool,
		Workers:    e.Downloader.Workers,
		Identity: &downloader.DownloadIdentity{
			VideoID:       info.ID,
			FormatID:      f.FormatID,
			ContentLength: clen,
		},
		Continue: e.Config.ContinuePartial,
		Progress: progressCb,
	}

	err := d.DownloadToFile(ctx, f.URL, dest)
	if err != nil && isForbidden(err) {
		if !e.Config.Quiet {
			color.Yellow("[%s] URL expired (403), re-extracting...", info.ID)
		}
		fresh, reextractErr := e.reextract(ctx, info)
		if reextractErr == nil {
			for _, freshFormat := range fresh.Formats {
				if freshFormat.FormatID == f.FormatID {
					// Retry with fresh URL (same identity, so resume state is preserved)
					return d.DownloadToFile(ctx, freshFormat.URL, dest)
				}
			}
		}
	}
	return err
}

// isForbidden reports whether an error indicates an HTTP 403 response.
func isForbidden(err error) bool {
	if err == nil {
		return false
	}
	var he *downloader.StatusError
	if errors.As(err, &he) && he.StatusCode == http.StatusForbidden {
		return true
	}
	// Fallback for non-typed errors
	return strings.Contains(err.Error(), "403")
}

// isRetryable reports whether an error is likely transient.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, downloader.ErrRateLimited) || errors.Is(err, downloader.ErrTransient) {
		return true
	}
	// Inspect network-level errors
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Temporary() || urlErr.Timeout() {
			return true
		}
	}
	// Fallback for non-typed errors
	msg := err.Error()
	if strings.Contains(msg, "429") || strings.Contains(msg, "503") || strings.Contains(msg, "504") {
		return true
	}
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "temporary") {
		return true
	}
	return false
}

// reportFailure calls the user-configured OnError callback if set.
func (e *Engine) reportFailure(f ytgo.DownloadFailure) {
	if e.Config.OnError != nil {
		e.Config.OnError(f)
	}
}

// log emits a structured debug log if a logger is configured.
func (e *Engine) log(msg string, attrs ...slog.Attr) {
	if e.Config.Logger == nil {
		return
	}
	e.Config.Logger.LogAttrs(context.Background(), slog.LevelDebug, msg, attrs...)
}

// reextract fetches fresh metadata for a video using the registered extractors.
func (e *Engine) reextract(ctx context.Context, info *extractor.VideoInfo) (*extractor.VideoInfo, error) {
	url := info.WebpageURL
	if url == "" {
		url = info.OriginalURL
	}
	if url == "" {
		return nil, fmt.Errorf("no URL available for re-extraction")
	}
	for _, ext := range e.Extractors {
		if ext.Suitable(url) {
			return ext.Extract(ctx, url)
		}
	}
	return nil, fmt.Errorf("no extractor found for re-extraction")
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
