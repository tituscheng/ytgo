package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFFmpegDownloader_buildArgs_UserAgent(t *testing.T) {
	fd := &FFmpegDownloader{
		Quiet:     true,
		UserAgent: "Mozilla/5.0 Test",
	}
	args := fd.buildArgs("https://example.com/playlist.m3u8", "/tmp/out.mp4")

	assert.Contains(t, args, "-user_agent")
	idx := indexOf(args, "-user_agent")
	require.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, "Mozilla/5.0 Test", args[idx+1])
	assert.Contains(t, args, "-i")
	assert.Contains(t, args, "https://example.com/playlist.m3u8")
	assert.Contains(t, args, "-bsf:a")
	assert.Contains(t, args, "aac_adtstoasc")
	// HLS smart defaults (independent of -N).
	assert.Contains(t, args, "-http_persistent")
	assert.Contains(t, args, "-http_multiple")
	assert.Equal(t, "1", args[indexOf(args, "-http_persistent")+1])
	assert.Equal(t, "1", args[indexOf(args, "-http_multiple")+1])
}

func TestFFmpegDownloader_buildArgs_NonHLS_NoSmartOpts(t *testing.T) {
	fd := &FFmpegDownloader{Quiet: true}
	args := fd.buildArgs("https://example.com/video.mpd", "/tmp/out.mkv")
	assert.NotContains(t, args, "-http_multiple")
	assert.NotContains(t, args, "-http_persistent")
}

func TestFFmpegDownloader_buildArgs_Headers(t *testing.T) {
	fd := &FFmpegDownloader{
		Quiet: true,
		Headers: map[string]string{
			"Origin":     "https://www.dailymotion.com",
			"User-Agent": "Mozilla/5.0 Test",
		},
	}
	args := fd.buildArgs("https://cdndirector.dailymotion.com/video.m3u8", "/tmp/out.mp4")

	assert.Contains(t, args, "-headers")
	idx := indexOf(args, "-headers")
	require.GreaterOrEqual(t, idx, 0)
	assert.Contains(t, args[idx+1], "Origin: https://www.dailymotion.com")
	assert.Contains(t, args[idx+1], "User-Agent: Mozilla/5.0 Test")
	assert.NotContains(t, args, "-user_agent")
}

func TestFFmpegDownloader_buildArgs_NoUserAgent(t *testing.T) {
	fd := &FFmpegDownloader{Quiet: true}
	args := fd.buildArgs("https://example.com/video.mpd", "/tmp/out.mkv")

	assert.NotContains(t, args, "-user_agent")
	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "mkv")
}

func TestFFmpegDownloader_DownloadToFile_QuietCapturesStderr(t *testing.T) {
	fd := &FFmpegDownloader{
		Quiet: true,
		Headers: map[string]string{
			"Origin":     "https://www.dailymotion.com",
			"User-Agent": "Mozilla/5.0 Test",
		},
		// Avoid multi-second backoff when probing an invalid host.
		MaxAttempts: 1,
	}
	err := fd.DownloadToFile(t.Context(), "https://invalid.example/notreal.m3u8", t.TempDir()+"/out.mp4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ffmpeg stream download")
}

func TestIsTransientStreamError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"ffmpeg stream download: exit status 8: HTTP error 504 Gateway Time-out", true},
		{"Server returned 5XX Server Error reply", true},
		{"HTTP 503 Service Unavailable", true},
		{"HTTP 502 Bad Gateway", true},
		{"HTTP 429 Too Many Requests", true},
		{"connection reset by peer", true},
		{"i/o timeout", true},
		{"context deadline exceeded", false},
		{"ffmpeg not found", false},
		{"Invalid data found when processing input", false},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransientStreamError(fmt.Errorf("%s", tc.msg)))
		})
	}
	assert.False(t, isTransientStreamError(nil))
}

func TestFFmpegDownloader_RetriesTransientThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	counterPath := filepath.Join(dir, "attempts")
	// Fake ffmpeg: fail twice with a 504-like message, then create the output.
	// Destination is the last argument.
	body := `#!/bin/sh
n=0
if [ -f "` + counterPath + `" ]; then
  n=$(cat "` + counterPath + `")
fi
n=$((n+1))
echo "$n" > "` + counterPath + `"
dest=""
for a in "$@"; do dest="$a"; done
if [ "$n" -lt 3 ]; then
  echo "HTTP error 504 Gateway Time-out" >&2
  exit 8
fi
printf 'ok' > "$dest"
exit 0
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	out := filepath.Join(dir, "out.mp4")
	fd := &FFmpegDownloader{
		FFmpegPath:  script,
		Quiet:       true,
		MaxAttempts: 3,
		RetryBase:   time.Millisecond,
	}
	err := fd.DownloadToFile(context.Background(), "https://example.com/pl.m3u8", out)
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(data))
	attempts, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	assert.Equal(t, "3", strings.TrimSpace(string(attempts)))
}

func TestFFmpegDownloader_NoRetryOnNonTransient(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	counterPath := filepath.Join(dir, "attempts")
	body := `#!/bin/sh
n=0
if [ -f "` + counterPath + `" ]; then n=$(cat "` + counterPath + `"); fi
n=$((n+1)); echo "$n" > "` + counterPath + `"
echo "Invalid data found when processing input" >&2
exit 1
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	fd := &FFmpegDownloader{
		FFmpegPath:  script,
		Quiet:       true,
		MaxAttempts: 3,
		RetryBase:   time.Millisecond,
	}
	err := fd.DownloadToFile(context.Background(), "https://example.com/pl.m3u8", filepath.Join(dir, "out.mp4"))
	require.Error(t, err)
	attempts, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	assert.Equal(t, "1", strings.TrimSpace(string(attempts)), "non-transient errors must not retry")
}

func indexOf(slice []string, target string) int {
	for i, s := range slice {
		if s == target {
			return i
		}
	}
	return -1
}
