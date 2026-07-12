package hlsfrag

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tituscheng/ytgo/internal/extractor/dailymotion"
)

func TestLiveDailymotionHLS380(t *testing.T) {
	if testing.Short() {
		t.Skip("live")
	}
	// Full hls-380 is ~300MB; allow enough wall time on typical broadband.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ext := dailymotion.NewExtractor(30 * time.Second)
	info, err := ext.Extract(ctx, "https://www.dailymotion.com/video/xah8252")
	if err != nil {
		t.Fatal(err)
	}
	var url string
	for _, f := range info.Formats {
		t.Logf("%s v=%v a=%v", f.FormatID, f.HasVideo, f.HasAudio)
		if f.FormatID == "hls-380" {
			url = f.URL
		}
	}
	if url == "" {
		t.Fatal("no hls-380")
	}
	dest := t.TempDir() + "/out.mp4"
	var last int64
	d := &Downloader{
		Workers:    12,
		ForceHTTP1: true,
		Continue:   true,
		Headers: map[string]string{
			"Origin":     "https://www.dailymotion.com",
			"Referer":    "https://www.dailymotion.com/",
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		},
		Progress: func(down, tot int64) {
			if down-last > 8_000_000 {
				t.Logf("progress %.1f / ~%.1f MB", float64(down)/1e6, float64(tot)/1e6)
				last = down
			}
		},
	}
	t0 := time.Now()
	err = d.DownloadToFile(ctx, url, dest)
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("download: %v after %s", err, elapsed)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	// Should get substantial data in 40s
	t.Logf("size=%.1f MB in %s (%.1f MB/s)", float64(st.Size())/1e6, elapsed, float64(st.Size())/1e6/elapsed.Seconds())
	if st.Size() < 5_000_000 {
		t.Fatalf("too small: %d", st.Size())
	}
}
