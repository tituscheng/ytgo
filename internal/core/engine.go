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

	"github.com/tituscheng/ytgo/internal/archive"
	"github.com/tituscheng/ytgo/internal/cleanup"
	"github.com/tituscheng/ytgo/internal/config"
	"github.com/tituscheng/ytgo/internal/downloader"
	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractor/youtube"
	"github.com/tituscheng/ytgo/internal/format"
	"github.com/tituscheng/ytgo/internal/limiter"
	"github.com/tituscheng/ytgo/internal/pipeline"
	"github.com/tituscheng/ytgo/internal/postprocessor"
	"github.com/tituscheng/ytgo/internal/subtitle"
	"github.com/tituscheng/ytgo/internal/template"
	"github.com/tituscheng/ytgo/internal/transport"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

// Engine runs the full download pipeline.
type Engine struct {
	Extractors    []extractor.InfoExtractor
	Downloader    *downloader.Downloader
	Config        config.DownloadOptions
	Transport     *http.Transport
	extractorName string

	onErrorMu    sync.Mutex
	onProgressMu sync.Mutex
}

// reportProgress invokes the user-configured OnProgress callback, if set.
// Calls are serialized so the callback need not be safe for concurrent use,
// even under concurrent playlist downloads or post-processing.
func (e *Engine) reportProgress(p ytgo.Progress) {
	if e.Config.OnProgress == nil {
		return
	}
	e.onProgressMu.Lock()
	defer e.onProgressMu.Unlock()
	e.Config.OnProgress(p)
}

// ffmpegProgress builds the callback handed to a post-processor for a given
// phase. It forwards ffmpeg's out_time (ms) as structured Progress events
// (against the known media duration) and updates the status spinner. It
// returns nil when nothing would consume progress, leaving the ffmpeg
// invocation unchanged in that case.
func (e *Engine) ffmpegProgress(info *extractor.VideoInfo, phase ytgo.Phase, label string, s *spinner.Spinner) func(outMs int64) {
	if e.Config.OnProgress == nil && s == nil {
		return nil
	}
	totMs := info.Duration.Milliseconds()
	return func(outMs int64) {
		e.reportProgress(ytgo.Progress{
			VideoID: info.ID,
			Title:   info.Title,
			Phase:   phase,
			Cur:     outMs,
			Tot:     totMs,
		})
		if s != nil && totMs > 0 {
			pct := float64(outMs) / float64(totMs) * 100
			if pct > 100 {
				pct = 100
			}
			s.Suffix = fmt.Sprintf("  %s (%.1f%%)", label, pct)
		}
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
	e.extractorName = ext.Name()

	if e.shouldEarlySkipExisting(rawURL) {
		videoID := youtube.ExtractVideoID(rawURL)
		if path, ok := e.lookupExistingMedia(videoID, true); ok {
			e.logExistingSkip(videoID, "", path, "existing media found")
			return nil, nil
		}
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
			color.Green("✓ Already in archive: %s", info.Title)
		}
		return nil, nil
	}

	if e.skipIfExistingMedia(info.ID, info.Title) {
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

	if e.skipIfOutputExists(outputPath, info.ID, info.Title) {
		return nil, nil
	}

	// --no-overwrites: skip if final file already exists
	if e.Config.NoOverwrites && !isStdout {
		if _, err := os.Stat(outputPath); err == nil {
			if !e.Config.Quiet {
				color.Green("✓ File already exists, skipping: %s", info.Title)
			}
			return nil, nil
		}
	}

	downloaded := make([]string, len(selected))

	if len(selected) == 1 {
		partPath := outputPath + ".part"
		finalPath := outputPath
		if isStdout {
			partPath = filepath.Join(os.TempDir(), filepath.Base(finalPath)) + ".part"
			finalPath = filepath.Join(os.TempDir(), filepath.Base(finalPath))
			temps.Push(partPath)
			temps.Push(finalPath)
		}
		if err := e.downloadFormatToFile(ctx, info, selected[0], partPath, 1, 1); err != nil {
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
		// Always rename (even in stdout mode) so that downloaded[] always points
		// to real files. The stdout path later copies from the final temp file.
		if err := os.Rename(partPath, finalPath); err != nil {
			return nil, fmt.Errorf("rename part file: %w", err)
		}
		if isStdout {
			temps.Pop() // finalPath (now the canonical temp file)
			temps.Pop() // partPath consumed by rename
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
				if err := e.downloadFormatToFile(gctx, info, f, partPath, i+1, len(selected)); err != nil {
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
				// Always rename (even in stdout mode) so that downloaded[] always points
				// to real files for the merger / final stdout copy.
				if err := os.Rename(partPath, finalPath); err != nil {
					return fmt.Errorf("rename part file: %w", err)
				}
				if isStdout {
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
		var mergeSpinner *spinner.Spinner
		if !e.Config.Quiet && !e.Config.NoProgress {
			mergeSpinner = newStatusSpinner("Merging video and audio...")
			mergeSpinner.Start()
		}
		merger.Progress = e.ffmpegProgress(info, ytgo.PhaseMerge, "Merging video and audio", mergeSpinner)
		merged, err := merger.Run(ctx, downloaded, outputPath, e.Config.MergeOutputFormat)
		if mergeSpinner != nil {
			mergeSpinner.Stop()
		}
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
	// Note: the converter stage has no resume identity, unlike the core
	// download (which uses the full segmented + resume machinery). Progress is
	// reported via ffmpeg's -progress output against the known media duration.
	if e.Config.ExtractAudio {
		conv := e.makeConverter(info)
		var convertSpinner *spinner.Spinner
		if !e.Config.Quiet && !e.Config.NoProgress {
			convertSpinner = newStatusSpinner("Extracting audio...")
			convertSpinner.Start()
		}
		conv.Progress = e.ffmpegProgress(info, ytgo.PhaseAudio, "Extracting audio", convertSpinner)
		converted, err := conv.ExtractAudio(ctx, finalPath, e.Config.AudioFormat, e.Config.AudioQuality)
		if convertSpinner != nil {
			convertSpinner.Stop()
		}
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
		printSaved(finalPath)
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

func (e *Engine) downloadFormatToFile(ctx context.Context, info *extractor.VideoInfo, f extractor.Format, dest string, current, total int) error {
	e.log("starting format download",
		slog.String("video_id", info.ID),
		slog.String("format_id", f.FormatID),
		slog.String("dest", dest),
		slog.Int64("filesize", f.Filesize))

	// For concurrent downloads (total > 1), use line-based status messages to
	// avoid spinner interleaving on stderr.
	concurrent := total > 1
	label := formatStreamLabel(f)
	var s *spinner.Spinner
	if !concurrent && !e.Config.Quiet && !e.Config.NoProgress {
		s = spinner.New(spinner.CharSets[14], 100*time.Millisecond)
		s.Suffix = fmt.Sprintf("  Downloading %s...", label)
		s.Start()
		defer s.Stop()
	} else if concurrent && !e.Config.Quiet {
		printDownloading(label)
		defer func() {
			printDownloadComplete(label)
		}()
	}

	clen := downloader.ParseContentLengthFromURL(f.URL)

	// Build the progress callback: structured event (one per format) + spinner.
	// Per-video aggregation across formats is the consumer's responsibility;
	// events carry FormatID so they can sum by VideoID if desired.
	progressCb := func(down, tot int64) {
		e.reportProgress(ytgo.Progress{
			VideoID:  info.ID,
			Title:    info.Title,
			Phase:    ytgo.PhaseDownload,
			FormatID: f.FormatID,
			Cur:      down,
			Tot:      tot,
		})
		if s != nil && tot > 0 {
			pct := float64(down) / float64(tot) * 100
			s.Suffix = fmt.Sprintf("  Downloading %s (%.1f%%)", label, pct)
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

	var err error
	err = e.downloadFormatURL(ctx, f, dest, d, progressCb)
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
					return e.downloadFormatURL(ctx, freshFormat, dest, d, progressCb)
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

func (e *Engine) downloadFormatURL(
	ctx context.Context,
	f extractor.Format,
	dest string,
	d *downloader.Downloader,
	progressCb downloader.ProgressFunc,
) error {
	if downloader.IsStreamManifest(f.URL) {
		return (&downloader.FFmpegDownloader{
			FFmpegPath: e.Config.FFmpegLocation,
			Quiet:      e.Config.Quiet,
			Progress:   progressCb,
		}).DownloadToFile(ctx, f.URL, dest)
	}
	return d.DownloadToFile(ctx, f.URL, dest)
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

func mergeContainerExt(selected []extractor.Format) string {
	// MKV accepts any codec combination via stream copy; use it when any stream
	// is in a WebM-native codec/container so merge does not fail on remux.
	for _, f := range selected {
		if f.Ext == "webm" || strings.HasPrefix(f.VideoCodec, "vp") || f.AudioCodec == "opus" || f.AudioCodec == "vorbis" {
			return "mkv"
		}
	}
	return "mp4"
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
	} else if e.Config.ExtractAudio && len(selected) > 1 {
		// Merge before audio extraction must use a video container; the final
		// audio extension is applied by ExtractAudio after merge.
		ext = mergeContainerExt(selected)
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
	var m *postprocessor.Merger
	if e.Config.MaxPostProcessors > 1 {
		m = postprocessor.NewMergerWithPrefix(e.Config.FFmpegLocation, fmt.Sprintf("[%s] ", info.ID))
	} else {
		m = postprocessor.NewMerger(e.Config.FFmpegLocation)
	}
	m.Quiet = e.Config.Quiet || !e.Config.Verbose
	return m
}

// makeConverter is the equivalent for audio extraction.
func (e *Engine) makeConverter(info *extractor.VideoInfo) *postprocessor.Converter {
	var c *postprocessor.Converter
	if e.Config.MaxPostProcessors > 1 {
		c = postprocessor.NewConverterWithPrefix(e.Config.FFmpegLocation, fmt.Sprintf("[%s] ", info.ID))
	} else {
		c = postprocessor.NewConverter(e.Config.FFmpegLocation)
	}
	c.Quiet = e.Config.Quiet || !e.Config.Verbose
	return c
}

// makeEmbedder is the equivalent for embed operations.
func (e *Engine) makeEmbedder(info *extractor.VideoInfo, client *http.Client) *postprocessor.Embedder {
	var emb *postprocessor.Embedder
	if e.Config.MaxPostProcessors > 1 {
		emb = postprocessor.NewEmbedderWithClientAndPrefix(e.Config.FFmpegLocation, client, fmt.Sprintf("[%s] ", info.ID))
	} else if client != nil {
		emb = postprocessor.NewEmbedderWithClient(e.Config.FFmpegLocation, client)
	} else {
		emb = postprocessor.NewEmbedder(e.Config.FFmpegLocation)
	}
	emb.Quiet = e.Config.Quiet || !e.Config.Verbose
	return emb
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
			// A missing track is an expected absence, not a failure: log it
			// for visibility but keep it out of the failure report.
			if errors.Is(ferr, subtitle.ErrNoTrack) {
				e.log("no subtitle track for requested language",
					slog.String("video_id", info.ID), slog.String("lang", lang))
				return
			}
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
	name := e.extractorName
	if name == "" {
		name = "unknown"
	}
	fmt.Fprintf(os.Stderr, "[%s] %s: Available formats\n", name, info.ID)
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
