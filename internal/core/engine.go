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
	"time"

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

	onErrorMu sync.Mutex
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

	eng := &Engine{
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

	if cfg.LimitRate > 0 {
		eng.log("rate limiting enabled", slog.Int64("bytes_per_sec", cfg.LimitRate))
	}

	return eng
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
		if err := e.writeSideFiles(ctx, info); err != nil {
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
		e.log("archive hit, skipping download",
			slog.String("video_id", info.ID),
			slog.String("title", info.Title))
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

	e.log("formats selected",
		slog.String("video_id", info.ID),
		slog.Int("count", len(selected)),
		slog.Any("format_ids", formatIDs(selected)))

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
		// Derive a cancellable context so that SIGINT / parent cancellation
		// properly aborts all concurrent format downloads (Issue 3).
		g, gctx := errgroup.WithContext(ctx)
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
				if err := e.downloadFormatToFile(gctx, info, f, partPath, i+1, len(selected), pa); err != nil {
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

	e.log("starting post-processing",
		slog.String("video_id", info.ID),
		slog.Int("files_to_merge", len(downloaded)),
		slog.Bool("extract_audio", e.Config.ExtractAudio),
		slog.Bool("embed", e.Config.EmbedThumbnail || e.Config.EmbedMetadata))

	finalPath := outputPath
	if len(downloaded) > 1 || e.Config.MergeOutputFormat != "" || isStdout {
		merger := e.makeMerger(info)
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
				e.cleanupFile(p)
			}
		}
	}

	// Audio extraction happens after merge/embed decisions.
	// Note: When using -x with a single audio format, some advanced
	// behaviors (resume identity on the converter stage, certain progress
	// aggregation) are intentionally reduced compared to normal downloads.
	// The core download still uses the full segmented + resume machinery.
	if e.Config.ExtractAudio {
		conv := e.makeConverter(info)
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
			e.cleanupFile(finalPath)
		}
		finalPath = converted
	}

	if e.Config.EmbedMetadata || e.Config.EmbedThumbnail || e.Config.EmbedSubs || e.Config.EmbedChapters {
		// Share the engine's tuned transport for thumbnail downloads during embedding.
		var embedClient *http.Client
		if e.Transport != nil {
			embedClient = &http.Client{Transport: e.Transport, Timeout: 30 * time.Second}
		}
		embedder := e.makeEmbedder(info, embedClient)
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
	if err := e.writeSideFiles(ctx, info); err != nil {
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
		e.cleanupFile(finalPath)
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
}

func (e *Engine) runPlaylist(ctx context.Context, info *extractor.VideoInfo) (*ytgo.PlaylistReport, error) {
	// Defensive limit against pathological or malicious playlist responses (Issue 7).
	const maxPlaylistEntries = 50000
	if len(info.Entries) > maxPlaylistEntries {
		return nil, fmt.Errorf("playlist too large (%d entries, maximum supported is %d)", len(info.Entries), maxPlaylistEntries)
	}

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
			var firstErr error
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
					if firstErr == nil {
						firstErr = err
					}
					if !e.Config.NoWarnings {
						color.Red("Error post-processing %s: %v", task.info.Title, err)
					}
				} else {
					reportMu.Lock()
					report.Succeeded++
					reportMu.Unlock()
				}
			}
			return firstErr
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
	e.log("starting format download",
		slog.String("video_id", info.ID),
		slog.String("format_id", f.FormatID),
		slog.String("dest", dest),
		slog.Int64("filesize", f.Filesize))

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
		Limiter:  e.Downloader.Limiter,
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
					e.log("403 recovery succeeded, retrying with fresh URL",
						slog.String("video_id", info.ID),
						slog.String("format_id", f.FormatID))
					// Retry with fresh URL (same identity, so resume state is preserved)
					return d.DownloadToFile(ctx, freshFormat.URL, dest)
				}
			}
			// Exact FormatID no longer present after re-extract (YouTube can rotate IDs).
			// Fall through and return the original 403 so the failure is recorded.
			e.log("403 recovery: exact format ID not found after re-extract",
				slog.String("video_id", info.ID),
				slog.String("format_id", f.FormatID),
				slog.Int("fresh_formats", len(fresh.Formats)))
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
// Calls are serialized so user code does not need its own mutex.
func (e *Engine) reportFailure(f ytgo.DownloadFailure) {
	if e.Config.OnError == nil {
		return
	}
	e.onErrorMu.Lock()
	defer e.onErrorMu.Unlock()
	e.Config.OnError(f)
}

// log emits a structured debug log if a logger is configured.
func (e *Engine) log(msg string, attrs ...slog.Attr) {
	if e.Config.Logger == nil {
		return
	}
	e.Config.Logger.LogAttrs(context.Background(), slog.LevelDebug, msg, attrs...)
}

// formatIDs is a tiny helper for structured logging.
func formatIDs(formats []extractor.Format) []string {
	ids := make([]string, len(formats))
	for i, f := range formats {
		ids[i] = f.FormatID
	}
	return ids
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

func (e *Engine) writeSideFiles(ctx context.Context, info *extractor.VideoInfo) error {
	// Respect --no-overwrites for all side files (Issue 6).
	// Mirrors the protection already applied to the main media file.
	if e.Config.WriteInfoJSON {
		path := template.BuildPath(e.Config.OutputTemplate, info, "info.json", e.Config.Paths)
		if e.shouldWriteSideFile(path) {
			data, _ := json.MarshalIndent(info, "", "  ")
			if err := os.WriteFile(path, data, 0644); err != nil {
				return err
			}
		}
	}
	if e.Config.WriteDescription {
		path := template.BuildPath(e.Config.OutputTemplate, info, "description", e.Config.Paths)
		if e.shouldWriteSideFile(path) {
			if err := os.WriteFile(path, []byte(info.Description), 0644); err != nil {
				return err
			}
		}
	}
	if e.Config.WriteThumbnail && len(info.Thumbnails) > 0 {
		path := template.BuildPath(e.Config.OutputTemplate, info, "jpg", e.Config.Paths)
		if e.shouldWriteSideFile(path) {
			thumbClient := &http.Client{Transport: e.Transport, Timeout: 30 * time.Second}
			if err := postprocessor.DownloadThumbnail(ctx, thumbClient, info.Thumbnails, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// shouldWriteSideFile returns true unless --no-overwrites is set and the
// target file already exists on disk.
func (e *Engine) shouldWriteSideFile(path string) bool {
	if !e.Config.NoOverwrites {
		return true
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// cleanupFile removes a file and logs at debug level on failure (Issue 10).
// Non-fatal; used for best-effort temp file removal.
func (e *Engine) cleanupFile(p string) {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		e.log("failed to remove temp file",
			slog.String("path", p),
			slog.String("error", err.Error()))
	}
}

// makeMerger returns a Merger, using a prefixed version when concurrent
// post-processing is enabled to prevent ffmpeg output interleaving.
func (e *Engine) makeMerger(info *extractor.VideoInfo) *postprocessor.Merger {
	if e.Config.MaxPostProcessors > 1 {
		return postprocessor.NewMergerWithPrefix(e.Config.FFmpegLocation, fmt.Sprintf("[%s] ", info.ID))
	}
	return postprocessor.NewMerger(e.Config.FFmpegLocation)
}

// makeConverter is the equivalent for audio extraction.
func (e *Engine) makeConverter(info *extractor.VideoInfo) *postprocessor.Converter {
	if e.Config.MaxPostProcessors > 1 {
		return postprocessor.NewConverterWithPrefix(e.Config.FFmpegLocation, fmt.Sprintf("[%s] ", info.ID))
	}
	return postprocessor.NewConverter(e.Config.FFmpegLocation)
}

// makeEmbedder is the equivalent for embed operations.
func (e *Engine) makeEmbedder(info *extractor.VideoInfo, client *http.Client) *postprocessor.Embedder {
	if e.Config.MaxPostProcessors > 1 {
		return postprocessor.NewEmbedderWithClientAndPrefix(e.Config.FFmpegLocation, client, fmt.Sprintf("[%s] ", info.ID))
	}
	if client != nil {
		return postprocessor.NewEmbedderWithClient(e.Config.FFmpegLocation, client)
	}
	return postprocessor.NewEmbedder(e.Config.FFmpegLocation)
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

	client := &http.Client{Transport: e.Transport, Timeout: 30 * time.Second}
	_, err := subtitle.WriteSubs(ctx, info, subtitle.WriteOptions{
		Langs:     langs,
		Format:    e.Config.SubFormat,
		BasePath:  e.Config.Paths,
		BaseName:  baseName,
		WriteAuto: e.Config.WriteAutoSubs,
		Client:    client,
		Logger:    e.Config.Logger,
		OnError: func(lang string, ferr error, retryable bool) {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				URL:       info.WebpageURL,
				Stage:     "subtitle",
				Error:     fmt.Sprintf("%s: %s", lang, ferr.Error()),
				Retryable: retryable,
			})
		},
	})
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
