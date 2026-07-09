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
		Quiet:  true,
		Headers: map[string]string{
			"Origin":     "https://www.dailymotion.com",
			"User-Agent": "Mozilla/5.0 Test",
		},
	}
	err := fd.DownloadToFile(t.Context(), "https://invalid.example/notreal.m3u8", t.TempDir()+"/out.mp4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ffmpeg stream download")
}

func indexOf(slice []string, target string) int {
	for i, s := range slice {
		if s == target {
			return i
		}
	}
	return -1
}
