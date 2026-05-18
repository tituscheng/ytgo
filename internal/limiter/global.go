// Package limiter provides rate limiting for ytgo downloads.
package limiter

import (
	"context"
	"io"
	"time"

	"golang.org/x/time/rate"
)

// GlobalLimiter wraps a golang.org/x/time/rate Limiter to enforce a global
// bytes-per-second cap across all concurrent downloads.
type GlobalLimiter struct {
	limiter *rate.Limiter
}

// NewGlobalLimiter creates a limiter with the given bytes-per-second cap.
// A rate <= 0 means unlimited.
func NewGlobalLimiter(bytesPerSec int64) *GlobalLimiter {
	if bytesPerSec <= 0 {
		return &GlobalLimiter{}
	}
	// Allow small bursts (e.g., 64 KB) to avoid throttling on every read.
	burst := int(bytesPerSec)
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	return &GlobalLimiter{
		limiter: rate.NewLimiter(rate.Limit(bytesPerSec), burst),
	}
}

// ThrottleReader wraps an io.ReadCloser so that every Read respects the rate
// limit while preserving the Close method.
func (gl *GlobalLimiter) ThrottleReader(ctx context.Context, r io.ReadCloser) io.ReadCloser {
	if gl.limiter == nil {
		return r
	}
	return &throttledReader{ctx: ctx, rc: r, limiter: gl.limiter}
}

type throttledReader struct {
	ctx     context.Context
	rc      io.ReadCloser
	limiter *rate.Limiter
}

func (tr *throttledReader) Read(p []byte) (int, error) {
	n, err := tr.rc.Read(p)
	if n > 0 && tr.limiter != nil {
		if r := tr.limiter.ReserveN(time.Now(), n); r.OK() {
			delay := r.Delay()
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-tr.ctx.Done():
					timer.Stop()
					return n, tr.ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	return n, err
}

func (tr *throttledReader) Close() error {
	return tr.rc.Close()
}
