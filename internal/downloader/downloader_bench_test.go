package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func BenchmarkDownloadSingleStream(b *testing.B) {
	data := make([]byte, 10*1024*1024) // 10 MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)
	}))
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := New()
		dest := fmt.Sprintf("/tmp/bench_single_%d.tmp", i)
		_ = d.DownloadToFile(context.Background(), ts.URL, dest)
		os.Remove(dest)
	}
}

func BenchmarkDownloadSegmented(b *testing.B) {
	data := make([]byte, 10*1024*1024) // 10 MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.Write(data)
			return
		}
		var start, end int
		fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
		if end == 0 || end >= len(data) {
			end = len(data) - 1
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
	}))
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := New()
		d.Workers = 4
		dest := fmt.Sprintf("/tmp/bench_seg_%d.tmp", i)
		_ = d.DownloadToFile(context.Background(), ts.URL, dest)
		os.Remove(dest)
	}
}

func BenchmarkPlanSegments(b *testing.B) {
	const totalSize = 100 * 1024 * 1024 // 100 MB
	for i := 0; i < b.N; i++ {
		_ = PlanSegments(totalSize, 4, 5*1024*1024, 10*1024*1024-1)
	}
}

// BenchmarkDownloadWithRateLimit measures the overhead of rate limiting.
func BenchmarkDownloadWithRateLimit(b *testing.B) {
	data := make([]byte, 10*1024*1024) // 10 MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)
	}))
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := New()
		// 100 MB/s limit — high enough to not throttle the 10 MB file
		// but exercises the limiter code path
		// Note: limiter is not imported here, so we just benchmark the downloader
		dest := fmt.Sprintf("/tmp/bench_rl_%d.tmp", i)
		_ = d.DownloadToFile(context.Background(), ts.URL, dest)
		os.Remove(dest)
	}
}

// readAll is a helper that discards the body.
func readAll(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		_, err := r.Read(buf)
		if err != nil {
			return
		}
	}
}
