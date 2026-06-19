package youtube

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractor/youtube/innertube"
)

func TestAppendManifestFormats_HLS(t *testing.T) {
	info := &extractor.VideoInfo{}
	resp := &innertube.PlayerResponse{
		VideoDetails: innertube.VideoDetails{IsLiveContent: true},
		StreamingData: innertube.StreamingData{
			HlsManifestURL:  "https://manifest.googlevideo.com/hls/playlist.m3u8",
			DashManifestURL: "https://manifest.googlevideo.com/dash/playlist.mpd",
		},
	}

	appendManifestFormats(info, resp)

	require.True(t, info.IsLiveContent)
	require.Len(t, info.Formats, 1)
	assert.Equal(t, "hls", info.Formats[0].FormatID)
	assert.Equal(t, "https://manifest.googlevideo.com/hls/playlist.m3u8", info.Formats[0].URL)
	assert.Equal(t, "https://manifest.googlevideo.com/hls/playlist.m3u8", info.Formats[0].ManifestURL)
	assert.True(t, info.Formats[0].HasVideo)
	assert.True(t, info.Formats[0].HasAudio)
}

func TestAppendManifestFormats_DASHOnly(t *testing.T) {
	info := &extractor.VideoInfo{}
	resp := &innertube.PlayerResponse{
		VideoDetails: innertube.VideoDetails{IsLiveContent: true},
		StreamingData: innertube.StreamingData{
			DashManifestURL: "https://manifest.googlevideo.com/dash/playlist.mpd",
		},
	}

	appendManifestFormats(info, resp)

	require.Len(t, info.Formats, 1)
	assert.Equal(t, "dash", info.Formats[0].FormatID)
	assert.Equal(t, "https://manifest.googlevideo.com/dash/playlist.mpd", info.Formats[0].URL)
}

func TestAppendManifestFormats_None(t *testing.T) {
	info := &extractor.VideoInfo{}
	resp := &innertube.PlayerResponse{
		VideoDetails:  innertube.VideoDetails{IsLiveContent: false},
		StreamingData: innertube.StreamingData{},
	}

	appendManifestFormats(info, resp)

	assert.False(t, info.IsLiveContent)
	assert.Empty(t, info.Formats)
}
