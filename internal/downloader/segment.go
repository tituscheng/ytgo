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
)

// SegmentDownloader downloads a single file using multiple concurrent HTTP Range
// requests. It supports resume via a sidecar JSON file and writes directly to
// the destination using WriteAt (pwrite on POSIX) so no temporary fragments are
// needed.
type SegmentDownloader struct {
	Client      *http.Client
	Workers     int   // max concurrent segment fetchers
	ChunkSize   int64 // minimum segment size (default 5 MB)
	MaxChunkSize int64 // maximum segment size (default ~10 MB)
	Progress    ProgressFunc
	BufferPool  *sync.Pool
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
	// Probe server capabilities
	totalSize, supportsRange, err := sd.probe(ctx, url)
	if err != nil || !supportsRange || totalSize <= 0 {
		return sd.fallback(ctx, url, destPath)
	}

	// Plan segments with bounded chunk sizes
	segments := PlanSegments(totalSize, sd.Workers, sd.ChunkSize, sd.MaxChunkSize)
	if len(segments) <= 1 {
		return sd.fallback(ctx, url, destPath)
	}

	// Load or create resume state
	rs, err := LoadResumeState(destPath)
	if err != nil {
		return fmt.Errorf("load resume state: %w", err)
	}
	if rs == nil {
		rs = &ResumeState{URL: url, DestPath: destPath, FileSize: totalSize}
	}

	// Determine missing work
	missing := rs.MissingRanges(segments)
	if len(missing) == 0 {
		// Already complete
		return rs.Remove()
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
		}
	} else {
		// Bounded concurrency via semaphore
		sem := make(chan struct{}, sd.Workers)
		var eg errgroup.Group
		var completedMu sync.Mutex

		for _, seg := range missing {
			seg := seg
			sem <- struct{}{}
			eg.Go(func() error {
				defer func() { <-sem }()
				if err := sd.fetchSegment(ctx, url, seg, fd, &downloaded); err != nil {
					return err
				}
				completedMu.Lock()
				rs.Completed = append(rs.Completed, seg)
				completedMu.Unlock()
				return nil
			})
		}

		if downloadErr := eg.Wait(); downloadErr != nil {
			_ = rs.Save()
			return fmt.Errorf("segment download failed: %w", downloadErr)
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
		return 0, false, fmt.Errorf("HEAD %d", resp.StatusCode)
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for range %s", resp.StatusCode, seg.String())
	}

	var buf []byte
	if sd.BufferPool != nil {
		buf = sd.BufferPool.Get().([]byte)
		defer sd.BufferPool.Put(buf)
	} else {
		buf = make([]byte, 32*1024)
	}

	offset := seg.StartByte
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := fd.WriteAt(buf[:n], offset); werr != nil {
				return fmt.Errorf("writeat %d: %w", offset, werr)
			}
			offset += int64(n)
			newTotal := downloaded.Add(int64(n))
			if sd.Progress != nil {
				sd.Progress(newTotal, seg.EndByte+1) // report against this segment's end
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
	}
	return d.Download(ctx, url, file)
}
