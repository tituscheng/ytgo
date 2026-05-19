package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"ytgo/internal/limiter"
)

// SegmentDownloader downloads a single file using multiple concurrent HTTP Range
// requests. It supports resume via a sidecar JSON file and writes directly to
// the destination using WriteAt (pwrite on POSIX) so no temporary fragments are
// needed.
type SegmentDownloader struct {
	Client       *http.Client
	Workers      int   // max concurrent segment fetchers
	ChunkSize    int64 // minimum segment size (default 5 MB)
	MaxChunkSize int64 // maximum segment size (default ~10 MB)
	Progress     ProgressFunc
	BufferPool   *sync.Pool
	Identity     *DownloadIdentity // nil = no resume validation
	Continue     bool              // default true; mirrors --no-continue
	Limiter      *limiter.GlobalLimiter
	totalSize    int64 // discovered file size (used for progress)
}

// NewSegmentDownloader creates a SegmentDownloader with sensible defaults.
func NewSegmentDownloader(client *http.Client) *SegmentDownloader {
	return &SegmentDownloader{
		Client:       client,
		Workers:      4,
		ChunkSize:    5 * 1024 * 1024,
		MaxChunkSize: defaultChunkSize,
	}
}

// DownloadToFile fetches url and writes it to destPath using segmented
// downloads. When Workers == 1, segments are downloaded sequentially.
func (sd *SegmentDownloader) DownloadToFile(ctx context.Context, url, destPath string) error {
	if sd.Workers < 1 {
		sd.Workers = 1
	}
	// Probe server capabilities
	totalSize, supportsRange, err := sd.probe(ctx, url)
	sd.totalSize = totalSize
	if err != nil || !supportsRange || totalSize <= 0 {
		return sd.fallback(ctx, url, destPath)
	}

	// Plan segments with bounded chunk sizes
	segments := PlanSegments(totalSize, sd.Workers, sd.ChunkSize, sd.MaxChunkSize)
	if len(segments) <= 1 {
		return sd.fallback(ctx, url, destPath)
	}

	// Handle --no-continue: wipe any partial state and start fresh
	if !sd.Continue {
		_ = os.Remove(destPath)
		_ = os.Remove(resumePath(destPath))
	}

	// Load or create resume state
	rs, err := LoadResumeState(destPath)
	if err != nil {
		return fmt.Errorf("load resume state: %w", err)
	}

	// Build expected identity for this download
	expectedCL := int64(0)
	if sd.Identity != nil {
		expectedCL = sd.Identity.ContentLength
	}
	if expectedCL == 0 {
		expectedCL = ParseContentLengthFromURL(url)
	}

	if rs == nil {
		rs = &ResumeState{
			URL:           url,
			DestPath:      destPath,
			FileSize:      totalSize,
			ContentLength: expectedCL,
		}
		if sd.Identity != nil {
			rs.VideoID = sd.Identity.VideoID
			rs.FormatID = sd.Identity.FormatID
		}
	} else if sd.Identity != nil {
		id := *sd.Identity
		id.ContentLength = expectedCL
		if !rs.Validate(id, url, totalSize) {
			// Stale state (wrong format, video, or size). Discard and start fresh.
			rs = &ResumeState{
				URL:           url,
				DestPath:      destPath,
				FileSize:      totalSize,
				VideoID:       sd.Identity.VideoID,
				FormatID:      sd.Identity.FormatID,
				ContentLength: expectedCL,
			}
		} else {
			// Valid state: update ephemeral URL in case it was refreshed
			rs.URL = url
			rs.FileSize = totalSize
		}
	}

	// Determine missing work
	missing := rs.MissingRanges(segments)
	if len(missing) == 0 {
		// Resume state claims complete, but verify the file actually exists
		// and is the expected size. The file may have been renamed or deleted
		// since the resume state was last written (e.g., worker crash).
		if fileSize(destPath) >= totalSize {
			return rs.Remove()
		}
		// Stale resume state — file missing or incomplete. Reset and re-download.
		rs = &ResumeState{
			URL:           url,
			DestPath:      destPath,
			FileSize:      totalSize,
			ContentLength: expectedCL,
		}
		if sd.Identity != nil {
			rs.VideoID = sd.Identity.VideoID
			rs.FormatID = sd.Identity.FormatID
		}
		missing = segments
	}

	// Pre-allocate destination file
	if existingSize := fileSize(destPath); existingSize < totalSize {
		if err := preallocate(destPath, totalSize); err != nil {
			return fmt.Errorf("preallocate: %w", err)
		}
	}

	fd, err := os.OpenFile(destPath, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open dest: %w", err)
	}
	defer fd.Close()

	// Atomic progress tracking
	var downloaded atomic.Int64

	if sd.Workers <= 1 {
		// Sequential download
		for _, seg := range missing {
			if err := sd.fetchSegment(ctx, url, seg, fd, &downloaded); err != nil {
				_ = rs.Save()
				return fmt.Errorf("segment download failed: %w", err)
			}
			rs.Completed = append(rs.Completed, seg)
			_ = rs.Save() // periodic save
		}
	} else {
		// Bounded concurrency via semaphore. The send is wrapped in a select
		// so context cancellation never blocks the launch loop (mirrors
		// pipeline.WorkerPool.Submit).
		sem := make(chan struct{}, sd.Workers)
		var eg errgroup.Group
		var completedMu sync.Mutex

	launch:
		for _, seg := range missing {
			seg := seg
			select {
			case <-ctx.Done():
				break launch
			case sem <- struct{}{}:
			}
			eg.Go(func() error {
				defer func() { <-sem }()
				if err := sd.fetchSegment(ctx, url, seg, fd, &downloaded); err != nil {
					return err
				}
				completedMu.Lock()
				rs.Completed = append(rs.Completed, seg)
				_ = rs.Save() // periodic save
				completedMu.Unlock()
				return nil
			})
		}

		if downloadErr := eg.Wait(); downloadErr != nil {
			_ = rs.Save()
			return fmt.Errorf("segment download failed: %w", downloadErr)
		}
		if err := ctx.Err(); err != nil {
			_ = rs.Save()
			return err
		}
	}

	_ = fd.Sync()
	_ = rs.Remove()
	return nil
}

// probe performs a HEAD request to discover Content-Length and Accept-Ranges.
func (sd *SegmentDownloader) probe(ctx context.Context, url string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := sd.Client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, false, &StatusError{StatusCode: resp.StatusCode, URL: url}
	}

	var size int64
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		fmt.Sscanf(cl, "%d", &size)
	}
	supports := resp.Header.Get("Accept-Ranges") == "bytes"
	return size, supports, nil
}

// fetchSegment downloads a single byte range and writes it to fd at the
// correct offset using WriteAt.
func (sd *SegmentDownloader) fetchSegment(ctx context.Context, url string, seg ByteRange, fd *os.File, downloaded *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", seg.String())

	resp, err := sd.Client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return &StatusError{StatusCode: resp.StatusCode, URL: url}
	}

	// Apply global rate limit (if configured) to the response body.
	// This is the hot path for all real downloads; the legacy writer path
	// already did this via ThrottleReader.
	var body io.ReadCloser = resp.Body
	if sd.Limiter != nil {
		body = sd.Limiter.ThrottleReader(ctx, resp.Body)
	}
	defer body.Close()

	var buf []byte
	if sd.BufferPool != nil {
		buf = sd.BufferPool.Get().([]byte)
		defer sd.BufferPool.Put(buf)
	} else {
		buf = make([]byte, 32*1024)
	}

	offset := seg.StartByte
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := fd.WriteAt(buf[:n], offset); werr != nil {
				return fmt.Errorf("writeat %d: %w", offset, werr)
			}
			offset += int64(n)
			newTotal := downloaded.Add(int64(n))
			if sd.Progress != nil {
				sd.Progress(newTotal, sd.totalSize) // report against global file size
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
	return nil
}

// fallback delegates to the standard single-stream downloader using bounded
// chunks. It opens the file for append and uses Downloader.Download directly
// to avoid recursion back into SegmentDownloader.
func (sd *SegmentDownloader) fallback(ctx context.Context, url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	file, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	d := &Downloader{
		Client:     sd.Client,
		Progress:   sd.Progress,
		BufferPool: sd.BufferPool,
		Limiter:    sd.Limiter,
	}
	return d.Download(ctx, url, file)
}
