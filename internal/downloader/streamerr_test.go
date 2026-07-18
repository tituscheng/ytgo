package downloader

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeStreamError(t *testing.T) {
	long := `ffmpeg stream download: exit status 8: [https @ 0xc81400000] HTTP error 504 Gateway Time-out
[in#0 @ 0xc81020000] Error opening input: Server returned 5XX Server Error reply
Error opening input file https://vod3.cf.dmcdn.net/sec2(abc)/video/fmp4/626112572/aac_q2_0/manifest.m3u8.
Error opening input files: Server returned 5XX Server Error reply`

	got := SummarizeStreamError(fmt.Errorf("%s", long))
	assert.Contains(t, got, "504")
	assert.Contains(t, got, "vod3.cf.dmcdn.net")
	assert.Contains(t, got, "manifest.m3u8")
	assert.NotContains(t, got, "\n")
	assert.Less(t, len(got), 160)

	assert.Equal(t, "HTTP 504", SummarizeStreamError(fmt.Errorf("fetch playlist: HTTP 504: <html>")))
	assert.Equal(t, "", SummarizeStreamError(nil))

	timeout := `fetch playlist: Get "https://vod3.cf.dmcdn.net/sec2(abc)/video/fmp4/1/aac_q1_0/manifest.m3u8": net/http: timeout awaiting response headers`
	gotTO := SummarizeStreamError(fmt.Errorf("%s", timeout))
	assert.Contains(t, gotTO, "timeout")
	assert.Contains(t, gotTO, "vod3.cf.dmcdn.net")
	assert.Contains(t, gotTO, "manifest.m3u8")
	assert.NotContains(t, gotTO, "sec2")
	assert.Less(t, len(gotTO), 120)
}
