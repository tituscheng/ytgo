package dailymotion

import (
	"context"
	"testing"
	"time"
)

func TestLiveExpandXA9dmfq(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ext := NewExtractor(30 * time.Second)
	info, err := ext.Extract(context.Background(), "https://www.dailymotion.com/video/xa9dmfq")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(info.Formats))
	for _, f := range info.Formats {
		ids = append(ids, f.FormatID)
		t.Logf("%s %dx%d v=%v a=%v", f.FormatID, f.Width, f.Height, f.HasVideo, f.HasAudio)
	}
	if len(info.Formats) < 2 {
		t.Fatalf("expected expanded formats, got %v", ids)
	}
	for _, id := range ids {
		if id == "hls-auto" {
			t.Fatalf("still hls-auto only: %v", ids)
		}
	}
}
