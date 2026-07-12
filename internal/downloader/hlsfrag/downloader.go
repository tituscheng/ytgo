package hlsfrag

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/tituscheng/ytgo/internal/transport"
)

// DefaultWorkers is used for HLS when ConcurrentFragments is unset/1.
// Dailymotion CDN scales well to ~8–16 connections on small fMP4 segments.
const DefaultWorkers = 12

// MaxWorkers caps adaptive/user concurrency.
const MaxWorkers = 32

// ProgressFunc reports bytes written so far and total estimate (0 if unknown).
type ProgressFunc func(downloaded, total int64)

// Downloader fetches an HLS media playlist and concatenates segments in order.
type Downloader struct {
	Client *http.Client
	// Workers is concurrent fragment fetchers. Values <=0 use DefaultWorkers.
	Workers int
	// Headers are applied to playlist and segment requests.
	Headers map[string]string
	// Progress is optional.
	Progress ProgressFunc
	// MaxRetries is per-fragment retries after the first attempt (default 3).
	MaxRetries int
	// ForceHTTP1 disables HTTP/2 (required for some Dailymotion CDN edges).
	ForceHTTP1 bool
	// Continue enables resume from a .hlsfrags sidecar (default true when zero-value
	// is treated as true only if explicitly set; callers should set Continue: true).
	// When false, any partial file and sidecar are discarded.
	Continue bool
}

// DownloadToFile downloads the media playlist at playlistURL into destPath.
// Output is a raw concatenation of init + media segments (valid fMP4 for
// Dailymotion-style playlists). Master playlists and encrypted/live streams
// return an error so callers can fall back to FFmpeg.
//
// Memory is bounded by a sliding fetch window (≈ 2×Workers completed-but-not-
// yet-written fragments). Partial progress is saved to destPath+".hlsfrags"
// so interrupted downloads can resume.
func (d *Downloader) DownloadToFile(ctx context.Context, playlistURL, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	client := d.httpClient()
	workers := d.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if workers > MaxWorkers {
		workers = MaxWorkers
	}
	retries := d.MaxRetries
	if retries <= 0 {
		retries = 3
	}

	return d.downloadPlaylist(ctx, client, playlistURL, destPath, workers, retries, 0)
}

func (d *Downloader) downloadPlaylist(
	ctx context.Context,
	client *http.Client,
	playlistURL, destPath string,
	workers, retries, depth int,
) error {
	plBody, err := d.getWithRetry(ctx, client, playlistURL, retries)
	if err != nil {
		return fmt.Errorf("fetch playlist: %w", err)
	}
	pl, err := ParseMediaPlaylist(bytes.NewReader(plBody), playlistURL)
	if err != nil {
		return err
	}
	// Multivariant master: follow the best STREAM-INF media playlist once.
	// Demuxed audio (EXT-X-MEDIA) is not muxed here — extract-time expand is
	// preferred for full A/V; this recovers video when expansion failed.
	if pl.IsMaster {
		if depth > 0 {
			return fmt.Errorf("nested master playlist not supported")
		}
		v := pl.BestVariant()
		if v == nil || v.URL == "" {
			return fmt.Errorf("master playlist has no usable variants")
		}
		return d.downloadPlaylist(ctx, client, v.URL, destPath, workers, retries, depth+1)
	}
	if pl.Encrypted {
		return fmt.Errorf("encrypted HLS not supported by hlsfrag")
	}
	if !pl.HasEndList {
		return fmt.Errorf("live/event HLS (no EXT-X-ENDLIST) not supported by hlsfrag")
	}
	if len(pl.Fragments) == 0 {
		return fmt.Errorf("empty media playlist")
	}

	start, bytesWritten, err := d.resolveResume(playlistURL, destPath, pl.Fragments)
	if err != nil {
		return err
	}
	if start >= len(pl.Fragments) {
		// Already complete from a prior run.
		removeResumeState(destPath)
		if d.Progress != nil {
			d.Progress(bytesWritten, bytesWritten)
		}
		return nil
	}

	flag := os.O_CREATE | os.O_WRONLY
	if start == 0 {
		flag |= os.O_TRUNC
	}
	fd, err := os.OpenFile(destPath, flag, 0o644)
	if err != nil {
		return fmt.Errorf("open dest: %w", err)
	}
	defer fd.Close()

	if start > 0 {
		if err := fd.Truncate(bytesWritten); err != nil {
			return fmt.Errorf("truncate partial: %w", err)
		}
		if _, err := fd.Seek(bytesWritten, io.SeekStart); err != nil {
			return fmt.Errorf("seek partial: %w", err)
		}
	}

	return d.downloadFragments(ctx, client, pl.Fragments, fd, destPath, playlistURL, start, bytesWritten, workers, retries)
}

// resolveResume returns the fragment index and byte offset to continue from.
func (d *Downloader) resolveResume(playlistURL, destPath string, frags []Fragment) (start int, bytesWritten int64, err error) {
	if !d.Continue {
		removeResumeState(destPath)
		return 0, 0, nil
	}
	st, loadErr := loadResumeState(destPath)
	if loadErr != nil {
		// Corrupt sidecar: start fresh.
		removeResumeState(destPath)
		return 0, 0, nil
	}
	if !validateResume(st, playlistURL, frags, destPath) {
		removeResumeState(destPath)
		return 0, 0, nil
	}
	return st.NextIndex, st.BytesWritten, nil
}

func fetchWindow(workers, remaining int) int {
	window := workers * 2
	if window < 16 {
		window = 16
	}
	if remaining > 0 && window > remaining {
		window = remaining
	}
	if window < 1 {
		window = 1
	}
	return window
}

func (d *Downloader) downloadFragments(
	ctx context.Context,
	client *http.Client,
	frags []Fragment,
	fd *os.File,
	destPath, playlistURL string,
	start int,
	bytesWritten int64,
	workers, retries int,
) error {
	n := len(frags)
	remaining := n - start
	window := fetchWindow(workers, remaining)

	// Ring of window slots: index i uses slots[i%window].
	// Sliding permits ensure at most `window` fragments are fetched ahead of
	// the ordered write head, bounding memory.
	//
	// Ownership: only one fragment occupies a ring slot at a time. The fetcher
	// fills data/err then closes done. The writer waits on done, drains the
	// slot, installs a fresh done channel, then releases a fetch permit so the
	// next occupant (i+window) may begin. Never replace a sync.Once under a
	// concurrent Do — that races after close(done) while Do is still returning.
	type slot struct {
		data []byte
		err  error
		done chan struct{}
	}
	slots := make([]slot, window)
	for i := range slots {
		slots[i].done = make(chan struct{})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// canFetch permits the producer to schedule the next fragment index.
	// Initially window permits; writer releases one after each successful write.
	canFetch := make(chan struct{}, window)
	for i := 0; i < window; i++ {
		canFetch <- struct{}{}
	}

	jobs := make(chan int)

	var written atomic.Int64
	written.Store(bytesWritten)

	// Producer: emit fragment indices start..n-1 with sliding-window backpressure.
	go func() {
		defer close(jobs)
		for i := start; i < n; i++ {
			select {
			case <-ctx.Done():
				return
			case <-canFetch:
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- i:
			}
		}
	}()

	// Fetch workers.
	g, gctx := errgroup.WithContext(ctx)
	for w := 0; w < workers; w++ {
		g.Go(func() error {
			for {
				select {
				case <-gctx.Done():
					return gctx.Err()
				case i, ok := <-jobs:
					if !ok {
						return nil
					}
					ring := i % window
					data, err := d.getWithRetry(gctx, client, frags[i].URL, retries)
					// Publish then signal. Writer must not recycle this slot
					// until it has observed done (and thus these stores).
					slots[ring].data = data
					slots[ring].err = err
					close(slots[ring].done)
					if err != nil {
						cancel()
						return fmt.Errorf("fragment %d: %w", frags[i].Index, err)
					}
				}
			}
		})
	}

	state := &ResumeState{
		Version:       resumeVersion,
		PlaylistURL:   playlistURL,
		FragmentCount: n,
		NextIndex:     start,
		BytesWritten:  bytesWritten,
		Fingerprint:   fragmentFingerprint(frags),
	}

	// Ordered writer.
	for i := start; i < n; i++ {
		ring := i % window
		select {
		case <-ctx.Done():
			cancel()
			_ = g.Wait()
			return ctx.Err()
		case <-slots[ring].done:
		}
		if slots[ring].err != nil {
			cancel()
			_ = g.Wait()
			return slots[ring].err
		}
		data := slots[ring].data
		if _, err := fd.Write(data); err != nil {
			cancel()
			_ = g.Wait()
			return fmt.Errorf("write fragment %d: %w", i, err)
		}
		w := written.Add(int64(len(data)))
		// Clear payload, install fresh done for the next occupant of this ring
		// index, then release a fetch permit (happens-before next fill).
		slots[ring].data = nil
		slots[ring].err = nil
		slots[ring].done = make(chan struct{})

		state.NextIndex = i + 1
		state.BytesWritten = w
		// Flush sidecar periodically (and on last fragment) to bound re-download
		// after a crash without fsyncing JSON on every tiny fMP4 segment.
		const resumeSaveEvery = 8
		if state.NextIndex == n || (state.NextIndex-start)%resumeSaveEvery == 0 {
			if err := saveResumeState(destPath, state); err != nil {
				cancel()
				_ = g.Wait()
				return fmt.Errorf("save resume state: %w", err)
			}
		}

		select {
		case canFetch <- struct{}{}:
		default:
			// Buffer full only if producer already exited; safe to drop.
		}

		if d.Progress != nil {
			// Estimate total from average bytes per written fragment so far.
			doneCount := int64(i - start + 1)
			avg := (w - bytesWritten) / doneCount
			estTotal := bytesWritten + avg*int64(remaining)
			if estTotal < w {
				estTotal = w
			}
			d.Progress(w, estTotal)
		}
	}

	if err := g.Wait(); err != nil && ctx.Err() == nil {
		return err
	}
	if err := fd.Sync(); err != nil {
		return err
	}
	removeResumeState(destPath)
	if d.Progress != nil {
		w := written.Load()
		d.Progress(w, w)
	}
	return nil
}

func (d *Downloader) httpClient() *http.Client {
	timeout := time.Duration(0)
	if d.Client != nil {
		timeout = d.Client.Timeout
	}

	if d.ForceHTTP1 {
		tr := transport.NewTunedTransport()
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(authority string, c *tls.Conn) http.RoundTripper{}
		tr.MaxIdleConnsPerHost = MaxWorkers + 4
		tr.MaxConnsPerHost = 0
		return &http.Client{Transport: tr, Timeout: timeout}
	}
	if d.Client != nil {
		return d.Client
	}
	return transport.NewTunedClient(timeout)
}

func (d *Downloader) get(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))
	}
	return io.ReadAll(resp.Body)
}

func (d *Downloader) getWithRetry(ctx context.Context, client *http.Client, rawURL string, retries int) ([]byte, error) {
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(100*(1<<(attempt-1))) * time.Millisecond
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		data, err := d.get(ctx, client, rawURL)
		if err == nil {
			return data, nil
		}
		last = err
	}
	return nil, last
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ResolveWorkers maps CLI ConcurrentFragments to HLS worker count.
// When n <= 1 (default / unspecified), returns DefaultWorkers for smart HLS.
// When n > 1, returns min(n, MaxWorkers) as an explicit user choice.
func ResolveWorkers(concurrentFragments int) int {
	if concurrentFragments > 1 {
		if concurrentFragments > MaxWorkers {
			return MaxWorkers
		}
		return concurrentFragments
	}
	return DefaultWorkers
}
