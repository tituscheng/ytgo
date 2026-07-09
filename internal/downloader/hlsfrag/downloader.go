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
	"sync"
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
}

// DownloadToFile downloads the media playlist at playlistURL into destPath.
// Output is a raw concatenation of init + media segments (valid fMP4 for
// Dailymotion-style playlists). Master playlists and encrypted/live streams
// return an error so callers can fall back to FFmpeg.
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

	fd, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open dest: %w", err)
	}
	defer fd.Close()

	return d.downloadFragments(ctx, client, pl.Fragments, fd, workers, retries)
}

func (d *Downloader) downloadFragments(
	ctx context.Context,
	client *http.Client,
	frags []Fragment,
	fd *os.File,
	workers, retries int,
) error {
	n := len(frags)
	// How far ahead of the write head fetchers may run (bounds memory).
	window := workers * 2
	if window < 16 {
		window = 16
	}
	if window > n {
		window = n
	}

	type slot struct {
		data []byte
		err  error
		once sync.Once
		done chan struct{}
	}
	slots := make([]slot, n)
	for i := range slots {
		slots[i].done = make(chan struct{})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Job queue: buffered by window so we never schedule more than `window`
	// indices before the writer has caught up enough for the producer to block.
	jobs := make(chan int, window)

	var written atomic.Int64

	// Producer: emit fragment indices.
	go func() {
		defer close(jobs)
		for i := 0; i < n; i++ {
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
					data, err := d.getWithRetry(gctx, client, frags[i].URL, retries)
					slots[i].once.Do(func() {
						slots[i].data = data
						slots[i].err = err
						close(slots[i].done)
					})
					if err != nil {
						cancel()
						return fmt.Errorf("fragment %d: %w", frags[i].Index, err)
					}
				}
			}
		})
	}

	// Ordered writer (must run concurrently with workers — not after launch).
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			cancel()
			_ = g.Wait()
			return ctx.Err()
		case <-slots[i].done:
		}
		if slots[i].err != nil {
			cancel()
			_ = g.Wait()
			return slots[i].err
		}
		data := slots[i].data
		if _, err := fd.Write(data); err != nil {
			cancel()
			_ = g.Wait()
			return fmt.Errorf("write fragment %d: %w", i, err)
		}
		w := written.Add(int64(len(data)))
		slots[i].data = nil
		if d.Progress != nil {
			avg := w / int64(i+1)
			d.Progress(w, avg*int64(n))
		}
	}

	if err := g.Wait(); err != nil && ctx.Err() == nil {
		return err
	}
	if err := fd.Sync(); err != nil {
		return err
	}
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
