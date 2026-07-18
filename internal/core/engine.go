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
	"github.com/tituscheng/ytgo/internal/downloader/hlsfrag"
	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractor/youtube"
	"github.com/tituscheng/ytgo/internal/extractor/youtube/innertube"
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
	Transport     http.RoundTripper
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
	baseTP := transport.NewTunedTransport()
	mediaRT := transport.WithHeaders(baseTP, map[string]string{
		"User-Agent": innertube.WebUserAgent,
	})

	eng := &Engine{
		Extractors: []extractor.InfoExtractor{},
		Downloader: &downloader.Downloader{
			Client:     &http.Client{Transport: mediaRT, Timeout: 0},
			BufferPool: &sync.Pool{New: func() any { return make([]byte, 32*1024) }},
			Workers:    cfg.ConcurrentFragments,
			Limiter:    limiter.NewGlobalLimiter(cfg.LimitRate),
		},
		Config:    cfg,
		Transport: mediaRT,
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
		printTagged(color.New(color.FgYellow), tagInfo, fmt.Sprintf("Extracting via %s: %s", ext.Name(), rawURL))
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
			printTagged(color.New(color.FgGreen), tagInfo, "Already in archive: "+info.Title)
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
		printTagged(color.New(color.FgCyan), tagInfo, fmt.Sprintf("Selected %d format(s)", len(selected)))
		for _, f := range selected {
			fmt.Fprintf(os.Stderr, "  %s: %dx%d %s/%s\n", f.FormatID, f.Width, f.Height, f.VideoCodec, f.AudioCodec)
		}
		if info.IsLiveContent && allFormatsLiveOrigin(selected) {
			e.log("live replay: selected formats use live-origin URLs; FFmpeg manifest routing may apply",
				slog.String("video_id", info.ID))
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
				printTagged(color.New(color.FgGreen), tagInfo, "File already exists, skipping: "+info.Title)
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
		used, err := e.downloadSingleFormatResilient(ctx, info, selected[0], partPath)
		if err != nil {
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				URL:       info.OriginalURL,
				FormatID:  selected[0].FormatID,
				Stage:     "download",
				Error:     downloader.SummarizeStreamError(err),
				Retryable: isRetryable(err),
			})
			return nil, fmt.Errorf("download format %s failed: %s", selected[0].FormatID, downloader.SummarizeStreamError(err))
		}
		if used.FormatID != selected[0].FormatID {
			selected[0] = used
		}
		// Always rename (even in stdout mode) so that downloaded[] always points
		// to real files. The stdout path later copies from the final temp file.
		if err := renamePartFile(partPath, finalPath); err != nil {
			return nil, err
		}
		if isStdout {
			temps.Pop() // finalPath (now the canonical temp file)
			temps.Pop() // partPath consumed by rename
		}
		downloaded[0] = finalPath
	} else if shouldSerializeFormats(info) {
		// Dailymotion (and similar CDNs) often 504 when video+audio media
		// playlists are opened in parallel. Download streams one at a time.
		var failParts []string
		for i, f := range selected {
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
			// total>1 enables line-based ↓/✓/✗ status (same as concurrent UI,
			// but streams run serially so CDN is not double-opened).
			if err := e.downloadFormatToFile(ctx, info, f, partPath, i+1, len(selected)); err != nil {
				summary := downloader.SummarizeStreamError(err)
				e.reportFailure(ytgo.DownloadFailure{
					VideoID:   info.ID,
					Title:     info.Title,
					URL:       info.OriginalURL,
					FormatID:  f.FormatID,
					Stage:     "download",
					Error:     summary,
					Retryable: isRetryable(err),
				})
				failParts = append(failParts, fmt.Sprintf("%s: %s", f.FormatID, summary))
				// Continue other streams so the user sees a full failure report
				// instead of aborting after the first CDN 504.
				continue
			}
			if err := renamePartFile(partPath, finalPath); err != nil {
				return nil, err
			}
			if isStdout {
				temps.Pop()
				temps.Pop()
			}
			downloaded[i] = finalPath
		}
		if len(failParts) > 0 {
			return nil, fmt.Errorf("download failed: %s", strings.Join(failParts, "; "))
		}
	} else {
		// Derive a cancellable context so that SIGINT / parent cancellation
		// properly aborts all concurrent format downloads (Issue 3).
		g, gctx := errgroup.WithContext(ctx)
		var failMu sync.Mutex
		var failParts []string
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
					summary := downloader.SummarizeStreamError(err)
					e.reportFailure(ytgo.DownloadFailure{
						VideoID:   info.ID,
						Title:     info.Title,
						URL:       info.OriginalURL,
						FormatID:  f.FormatID,
						Stage:     "download",
						Error:     summary,
						Retryable: isRetryable(err),
					})
					failMu.Lock()
					failParts = append(failParts, fmt.Sprintf("%s: %s", f.FormatID, summary))
					failMu.Unlock()
					// Cancel siblings via errgroup, but remember every failure
					// observed before cancel for a consolidated final message.
					return fmt.Errorf("%s: %s", f.FormatID, summary)
				}
				// Always rename (even in stdout mode) so that downloaded[] always points
				// to real files for the merger / final stdout copy.
				if err := renamePartFile(partPath, finalPath); err != nil {
					return err
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
			failMu.Lock()
			parts := append([]string(nil), failParts...)
			failMu.Unlock()
			if len(parts) > 0 {
				return nil, fmt.Errorf("download failed: %s", strings.Join(parts, "; "))
			}
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
			mergeSpinner = newStatusSpinner("[merge] Merging video and audio...")
			mergeSpinner.Start()
		}
		merger.Progress = e.ffmpegProgress(info, ytgo.PhaseMerge, "[merge] Merging video and audio", mergeSpinner)
		merged, err := merger.Run(ctx, downloaded, outputPath, e.Config.MergeOutputFormat)
		if mergeSpinner != nil {
			mergeSpinner.Stop()
		}
		if err != nil {
			summary := downloader.SummarizeStreamError(err)
			e.reportFailure(ytgo.DownloadFailure{
				VideoID:   info.ID,
				Title:     info.Title,
				Stage:     "merge",
				Error:     summary,
				Retryable: false,
			})
			return fmt.Errorf("merge failed: %s", summary)
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
			convertSpinner = newStatusSpinner("[info] Extracting audio...")
			convertSpinner.Start()
		}
		conv.Progress = e.ffmpegProgress(info, ytgo.PhaseAudio, "[info] Extracting audio", convertSpinner)
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

	printTagged(color.New(color.FgCyan), tagInfo,
		fmt.Sprintf("Playlist: %s (%d entries)", info.PlaylistTitle, len(info.Entries)))

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
						printTagged(color.New(color.FgRed), tagError,
							fmt.Sprintf("post-processing %s: %s", task.info.Title, downloader.SummarizeStreamError(err)))
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
					printTagged(color.New(color.FgRed), tagError,
						fmt.Sprintf("downloading %s: %s", job.Title, downloader.SummarizeStreamError(err)))
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
		s.Suffix = fmt.Sprintf("  [download] Downloading %s...", label)
		s.Start()
		defer s.Stop()
	} else if concurrent && !e.Config.Quiet {
		printDownloading(label)
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
			s.Suffix = fmt.Sprintf("  [download] Downloading %s (%.1f%%)", label, pct)
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

	err := e.downloadFormatURL(ctx, info, f, dest, d, progressCb)
	if err != nil && isForbidden(err) {
		if !e.Config.Quiet {
			printRetry("URL expired (403), re-extracting...")
		}
		fresh, reextractErr := e.reextract(ctx, info)
		if reextractErr == nil {
			matched := false
			for _, freshFormat := range fresh.Formats {
				if freshFormat.FormatID == f.FormatID {
					matched = true
					e.log("403 recovery: retrying with fresh URL",
						slog.String("video_id", info.ID),
						slog.String("format_id", f.FormatID))
					err = e.downloadFormatURL(ctx, fresh, freshFormat, dest, d, progressCb)
					break
				}
			}
			if !matched {
				// Exact FormatID no longer present after re-extract (YouTube can rotate IDs).
				// Keep the original 403 so the failure is recorded.
				e.log("403 recovery: exact format ID not found after re-extract",
					slog.String("video_id", info.ID),
					slog.String("format_id", f.FormatID),
					slog.Int("fresh_formats", len(fresh.Formats)))
			}
		}
	}

	// Concurrent mode: report outcome only after the download finishes so a
	// failed stream never shows a green checkmark (defer would always fire).
	if concurrent && !e.Config.Quiet {
		if err != nil {
			printDownloadFailed(label, downloader.SummarizeStreamError(err))
		} else {
			printDownloadComplete(label)
		}
	}
	return err
}

func (e *Engine) downloadFormatURL(
	ctx context.Context,
	info *extractor.VideoInfo,
	f extractor.Format,
	dest string,
	d *downloader.Downloader,
	progressCb downloader.ProgressFunc,
) error {
	url, isManifest := resolveDownloadURL(info, f)
	if isManifest {
		// Prefer native concurrent HLS for VOD media playlists (e.g. Dailymotion
		// fMP4). Fall back to FFmpeg for masters, live, encrypted, or when native
		// fails. Dailymotion CDN 5xx used to skip FFmpeg (same edge, log noise),
		// but FFmpeg's sequential/HLS client often still succeeds after native
		// multi-connection pressure triggers 503/504 — so fall back after a
		// short cool-down (Quiet keeps ffmpeg stderr out of the UI).
		if strings.Contains(strings.ToLower(url), ".m3u8") && !info.IsLiveContent {
			if err := e.hlsFragDownload(ctx, info, url, dest, progressCb); err == nil {
				return nil
			} else if ctx.Err() != nil {
				// Don't fall back to FFmpeg after user cancel / deadline.
				return err
			} else {
				summary := downloader.SummarizeStreamError(err)
				e.log("native HLS download failed, falling back to ffmpeg",
					slog.String("video_id", info.ID),
					slog.String("format_id", f.FormatID),
					slog.String("error", summary),
					slog.Bool("transient", downloader.IsTransientStreamError(err)))
				if isDailymotionInfo(info) && downloader.IsTransientStreamError(err) {
					// Native partial/TS concat + .hlsfrags are not usable by FFmpeg.
					clearNativeHLSPartial(dest)
					if !e.Config.Quiet {
						printRetry(fmt.Sprintf("CDN %s; retrying via FFmpeg…", summary))
					}
					if err := sleepCtx(ctx, 1500*time.Millisecond); err != nil {
						return err
					}
				}
			}
		}
		return e.ffmpegDownloader(progressCb, ffmpegDownloadHeaders(info, url)).DownloadToFile(ctx, url, dest)
	}

	err := d.DownloadToFile(ctx, url, dest)
	if err != nil && info.IsLiveContent {
		if fallback := manifestFormat(info); fallback != nil {
			return e.ffmpegDownloader(progressCb, ffmpegDownloadHeaders(info, fallback.URL)).DownloadToFile(ctx, fallback.URL, dest)
		}
	}
	return err
}

// hlsFragDownload fetches a media playlist with concurrent fragment workers.
// ConcurrentFragments <= 1 enables smart default workers (hlsfrag.DefaultWorkers);
// values > 1 are treated as an explicit user choice.
func (e *Engine) hlsFragDownload(
	ctx context.Context,
	info *extractor.VideoInfo,
	playlistURL, dest string,
	progressCb downloader.ProgressFunc,
) error {
	headers := ffmpegDownloadHeaders(info, playlistURL)
	if headers == nil {
		headers = map[string]string{}
	}
	if headers["User-Agent"] == "" {
		headers["User-Agent"] = innertube.WebUserAgent
	}
	// Dailymotion (and some CDNs) reject HTTP/2 on playlist/segment edges.
	forceH1 := isDailymotionInfo(info) ||
		strings.Contains(playlistURL, "dailymotion.com") ||
		strings.Contains(playlistURL, "dmcdn.net")

	workers := hlsfrag.ResolveWorkers(e.Config.ConcurrentFragments)
	// Soften parallel fragment pressure on fragile DM edges when downloading
	// high-worker defaults (still concurrent, just less aggressive).
	// 503/504 storms are common on vod*.cf.dmcdn.net with >4 parallel GETs.
	if isDailymotionInfo(info) && e.Config.ConcurrentFragments <= 1 && workers > 4 {
		workers = 4
	}
	maxRetries := 0 // hlsfrag default
	if isDailymotionInfo(info) {
		// Playlist/segment edges flake; give native more attempts before FFmpeg.
		maxRetries = 6
	}
	e.log("native HLS fragment download",
		slog.String("video_id", info.ID),
		slog.Int("workers", workers),
		slog.Int("max_retries", maxRetries),
		slog.Bool("force_http1", forceH1))

	hd := &hlsfrag.Downloader{
		Client:     e.Downloader.Client,
		Workers:    workers,
		Headers:    headers,
		ForceHTTP1: forceH1,
		Continue:   e.Config.ContinuePartial,
		Progress:   hlsfrag.ProgressFunc(progressCb),
		MaxRetries: maxRetries,
	}
	if err := hd.DownloadToFile(ctx, playlistURL, dest); err != nil {
		return err
	}
	// Classic MPEG-TS HLS (e.g. some Dailymotion VODs) concatenates to a
	// transport stream. Remux to a real MP4 when the output name promises one.
	return e.remuxNativeHLSIfMPEGTS(ctx, dest)
}

// remuxNativeHLSIfMPEGTS turns raw TS fragment concat into a proper MP4 when
// dest is named *.mp4 (or *.part of one). No-op for fMP4 native downloads.
func (e *Engine) remuxNativeHLSIfMPEGTS(ctx context.Context, dest string) error {
	if !downloader.IsMPEGTSFile(dest) {
		return nil
	}
	if !e.Config.Quiet {
		printTagged(color.New(color.FgCyan), tagInfo, "Remuxing MPEG-TS → MP4…")
	}
	e.log("remuxing native HLS MPEG-TS to MP4",
		slog.String("dest", dest))
	if err := downloader.RemuxMPEGTSToMP4(ctx, e.Config.FFmpegLocation, dest); err != nil {
		return err
	}
	return nil
}

func (e *Engine) ffmpegDownloader(progressCb downloader.ProgressFunc, headers map[string]string) *downloader.FFmpegDownloader {
	// Always Quiet: never stream raw ffmpeg logs. Retry notices only with -v.
	return &downloader.FFmpegDownloader{
		FFmpegPath: e.Config.FFmpegLocation,
		Quiet:      true,
		LogRetries: e.Config.Verbose && !e.Config.Quiet,
		Progress:   progressCb,
		UserAgent:  innertube.WebUserAgent,
		Headers:    headers,
	}
}

// dailymotionMediaUA matches the browser profile yt-dlp uses for DM CDN GETs.
const dailymotionMediaUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

// dmSingleFormatCDNRetries is how many times to re-extract + retry the same
// format after a transient CDN failure (native + FFmpeg already tried inside
// downloadFormatURL). Separate from per-request fragment retries.
const dmSingleFormatCDNRetries = 1

func isDailymotionInfo(info *extractor.VideoInfo) bool {
	if info == nil {
		return false
	}
	return strings.Contains(info.WebpageURL, "dailymotion.com") ||
		strings.Contains(info.OriginalURL, "dailymotion.com") ||
		strings.Contains(info.OriginalURL, "dai.ly")
}

// downloadSingleFormatResilient downloads one selected format. On Dailymotion
// CDN 5xx it re-extracts fresh signed URLs and, if still failing, tries lower
// single-mux HLS qualities (e.g. hls-480 → hls-380).
func (e *Engine) downloadSingleFormatResilient(
	ctx context.Context,
	info *extractor.VideoInfo,
	f extractor.Format,
	dest string,
) (extractor.Format, error) {
	err := e.downloadFormatToFile(ctx, info, f, dest, 1, 1)
	if err == nil {
		return f, nil
	}
	if ctx.Err() != nil {
		return f, err
	}
	if !isDailymotionInfo(info) || !downloader.IsTransientStreamError(err) {
		return f, err
	}

	last := err
	working := info
	cur := f

	for attempt := 1; attempt <= dmSingleFormatCDNRetries; attempt++ {
		if err := sleepCtx(ctx, time.Duration(attempt)*2*time.Second); err != nil {
			return cur, err
		}
		if !e.Config.Quiet {
			printRetry(fmt.Sprintf("CDN %s; re-extracting and retrying %s…",
				downloader.SummarizeStreamError(last), cur.FormatID))
		}
		fresh, reErr := e.reextract(ctx, working)
		if reErr != nil {
			e.log("CDN recovery re-extract failed",
				slog.String("video_id", info.ID),
				slog.String("error", reErr.Error()))
			continue
		}
		working = fresh
		matched := findFormatByID(fresh.Formats, cur.FormatID)
		if matched == nil {
			e.log("CDN recovery: format missing after re-extract",
				slog.String("video_id", info.ID),
				slog.String("format_id", cur.FormatID))
			continue
		}
		clearNativeHLSPartial(dest)
		last = e.downloadFormatToFile(ctx, fresh, *matched, dest, 1, 1)
		if last == nil {
			return *matched, nil
		}
		if ctx.Err() != nil {
			return cur, last
		}
		if !downloader.IsTransientStreamError(last) {
			return cur, last
		}
		cur = *matched
	}

	// Quality ladder: only when the user asked for a generic best-style pick.
	if !allowsQualityLadderFallback(e.Config.Format) {
		return cur, last
	}
	for _, alt := range lowerMuxedHLSFormats(cur, working.Formats) {
		if err := sleepCtx(ctx, 1500*time.Millisecond); err != nil {
			return cur, err
		}
		if !e.Config.Quiet {
			printRetry(fmt.Sprintf("CDN %s; trying lower quality %s…",
				downloader.SummarizeStreamError(last), alt.FormatID))
		}
		clearNativeHLSPartial(dest)
		last = e.downloadFormatToFile(ctx, working, alt, dest, 1, 1)
		if last == nil {
			return alt, nil
		}
		if ctx.Err() != nil {
			return cur, last
		}
		if !downloader.IsTransientStreamError(last) {
			return cur, last
		}
	}
	return cur, last
}

func findFormatByID(formats []extractor.Format, id string) *extractor.Format {
	for i := range formats {
		if formats[i].FormatID == id {
			return &formats[i]
		}
	}
	return nil
}

// allowsQualityLadderFallback is true for default / best-style selectors where
// silently taking a lower rung is acceptable. Exact format IDs (-f hls-480)
// must not be substituted.
func allowsQualityLadderFallback(selector string) bool {
	s := strings.TrimSpace(strings.ToLower(selector))
	if s == "" || s == "best" || s == "b" {
		return true
	}
	// best[height<=720] / bestvideo+bestaudio style still want "best available".
	if strings.HasPrefix(s, "best") || strings.HasPrefix(s, "bv") {
		return true
	}
	return false
}

// lowerMuxedHLSFormats returns other single-file HLS formats (video+audio) with
// lower or equal height / TBR than primary, best-first among remaining.
func lowerMuxedHLSFormats(primary extractor.Format, all []extractor.Format) []extractor.Format {
	var alts []extractor.Format
	for _, f := range all {
		if f.FormatID == primary.FormatID {
			continue
		}
		if !strings.HasPrefix(f.FormatID, "hls-") {
			continue
		}
		// Skip demuxed audio-only or video-only rungs.
		if !f.HasVideo || !f.HasAudio {
			continue
		}
		if primary.Height > 0 && f.Height > primary.Height {
			continue
		}
		if primary.Height > 0 && f.Height == primary.Height && f.TBR > primary.TBR {
			continue
		}
		if primary.Height <= 0 && primary.TBR > 0 && f.TBR > primary.TBR {
			continue
		}
		alts = append(alts, f)
	}
	// Prefer higher remaining quality first.
	for i := 0; i < len(alts); i++ {
		for j := i + 1; j < len(alts); j++ {
			if formatQualityScore(alts[j]) > formatQualityScore(alts[i]) {
				alts[i], alts[j] = alts[j], alts[i]
			}
		}
	}
	return alts
}

func formatQualityScore(f extractor.Format) float64 {
	h := float64(f.Height)
	if h <= 0 {
		h = f.TBR
	}
	return h*1000 + f.TBR
}

func clearNativeHLSPartial(dest string) {
	_ = os.Remove(dest)
	_ = os.Remove(dest + ".hlsfrags")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// shouldSerializeFormats is true when parallel A/V downloads tend to trip the CDN.
func shouldSerializeFormats(info *extractor.VideoInfo) bool {
	return isDailymotionInfo(info)
}

func ffmpegDownloadHeaders(info *extractor.VideoInfo, streamURL string) map[string]string {
	if isDailymotionInfo(info) ||
		strings.Contains(streamURL, "dailymotion.com") ||
		strings.Contains(streamURL, "dmcdn.net") {
		// Match yt-dlp media requests: no Origin (avoids some CF edge paths),
		// browser Accept / Sec-Fetch-Mode, Windows Chrome UA.
		return map[string]string{
			"User-Agent":      dailymotionMediaUA,
			"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"Accept-Language": "en-us,en;q=0.5",
			"Sec-Fetch-Mode":  "navigate",
			"Referer":         "https://www.dailymotion.com/",
		}
	}
	return nil
}

func manifestFormat(info *extractor.VideoInfo) *extractor.Format {
	for i := range info.Formats {
		if info.Formats[i].FormatID == "hls" {
			return &info.Formats[i]
		}
	}
	for i := range info.Formats {
		if info.Formats[i].FormatID == "dash" {
			return &info.Formats[i]
		}
	}
	return nil
}

func resolveDownloadURL(info *extractor.VideoInfo, f extractor.Format) (string, bool) {
	if downloader.IsStreamManifest(f.URL) {
		return f.URL, true
	}
	if strings.Contains(f.URL, "live=1") {
		if m := manifestFormat(info); m != nil {
			return m.URL, true
		}
		return f.URL, true
	}
	if info.IsLiveContent && f.Filesize == 0 && f.HasVideo {
		if m := manifestFormat(info); m != nil {
			return m.URL, true
		}
	}
	return f.URL, false
}

func allFormatsLiveOrigin(formats []extractor.Format) bool {
	if len(formats) == 0 {
		return false
	}
	for _, f := range formats {
		if !strings.Contains(f.URL, "live=1") && !downloader.IsStreamManifest(f.URL) {
			return false
		}
	}
	return true
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

// renamePartFile atomically promotes a successful download from *.part to its
// final name. If the .part file is already gone but the final path exists and
// is non-empty (e.g. crash between rename and return, or a concurrent process
// finished first), treat that as success so retries stay idempotent.
func renamePartFile(partPath, finalPath string) error {
	err := os.Rename(partPath, finalPath)
	if err == nil {
		return nil
	}

	// Idempotent: another path already produced a non-empty final file and
	// removed the part (crash between rename and return, or concurrent ytgo).
	if st, ferr := os.Stat(finalPath); ferr == nil && st.Size() > 0 {
		if _, perr := os.Stat(partPath); os.IsNotExist(perr) {
			return nil
		}
	}

	if _, perr := os.Stat(partPath); os.IsNotExist(perr) {
		return fmt.Errorf("rename part file: %s: no such file or directory (download may have been removed or raced with another process)", partPath)
	}
	return fmt.Errorf("rename part file: %w", err)
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
	fmt.Fprintf(os.Stderr, "[info] %s %s: available formats\n", name, info.ID)
	for _, f := range info.Formats {
		codec := f.VideoCodec
		if f.AudioCodec != "" && codec != "" {
			codec += ", " + f.AudioCodec
		} else if f.AudioCodec != "" {
			codec = f.AudioCodec
		}
		size := f.Filesize
		if size <= 0 {
			size = f.FilesizeApprox
		}
		fmt.Fprintf(os.Stderr, "%-5s %-10s %-6s %dx%d %s ~%s\n",
			f.FormatID, f.Ext, f.QualityLabel, f.Width, f.Height, codec, humanSize(size))
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
