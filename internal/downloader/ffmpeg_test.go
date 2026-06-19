package downloader

import (
	"testing"

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
}

func TestFFmpegDownloader_buildArgs_NoUserAgent(t *testing.T) {
	fd := &FFmpegDownloader{Quiet: true}
	args := fd.buildArgs("https://example.com/video.mpd", "/tmp/out.mkv")

	assert.NotContains(t, args, "-user_agent")
	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "mkv")
}

func indexOf(slice []string, target string) int {
	for i, s := range slice {
		if s == target {
			return i
		}
	}
	return -1
}
