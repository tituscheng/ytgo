package dailymotion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
)

const demuxedMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="0_aac_q1",NAME="(original)",AUTOSELECT=YES,DEFAULT=YES,URI="https://vod.example/aac_q1/manifest.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="0_aac_q2",NAME="(original)",AUTOSELECT=YES,DEFAULT=YES,URI="https://vod.example/aac_q2/manifest.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=836280,CODECS="mp4a.40.2,avc1.64001f",RESOLUTION=480x848,NAME="480",AUDIO="0_aac_q2"
https://vod.example/h264_hq/manifest.m3u8#cell=cf3
#EXT-X-STREAM-INF:BANDWIDTH=460560,CODECS="mp4a.40.2,avc1.42001e",RESOLUTION=360x640,NAME="380",AUDIO="0_aac_q1"
https://vod.example/h264_sd/manifest.m3u8#cell=cf3
`

const muxedMaster = `#EXTM3U
#EXT-X-STREAM-INF:RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2",BANDWIDTH=3600000
stream_720.m3u8
#EXT-X-STREAM-INF:RESOLUTION=854x480,CODECS="avc1.4d401e,mp4a.40.2",BANDWIDTH=1200000
stream_480.m3u8
`

func TestParseMasterPlaylistDemuxed(t *testing.T) {
	result, err := parseMasterPlaylist(strings.NewReader(demuxedMaster), "https://cdndirector.example/master.m3u8")
	require.NoError(t, err)

	byID := indexFormats(result.formats)
	require.Contains(t, byID, "hls-480")
	require.Contains(t, byID, "hls-380")
	require.Contains(t, byID, "hls-aac-q1")
	require.Contains(t, byID, "hls-aac-q2")

	v480 := byID["hls-480"]
	assert.True(t, v480.HasVideo)
	assert.False(t, v480.HasAudio)
	assert.Equal(t, 480, v480.Width)
	assert.Equal(t, 848, v480.Height)
	assert.InDelta(t, 836.28, v480.TBR, 0.01)
	assert.Equal(t, "avc1.64001f", v480.VideoCodec)
	assert.Equal(t, "https://vod.example/h264_hq/manifest.m3u8", v480.URL)
	assert.NotContains(t, v480.URL, "#")

	a2 := byID["hls-aac-q2"]
	assert.False(t, a2.HasVideo)
	assert.True(t, a2.HasAudio)
	assert.Equal(t, "https://vod.example/aac_q2/manifest.m3u8", a2.URL)
	assert.Equal(t, 128.0, a2.ABR)
}

func TestParseMasterPlaylistMuxed(t *testing.T) {
	result, err := parseMasterPlaylist(strings.NewReader(muxedMaster), "https://cdn.example/master.m3u8")
	require.NoError(t, err)
	require.Len(t, result.formats, 2)

	byID := indexFormats(result.formats)
	f720 := byID["hls-720"]
	assert.True(t, f720.HasVideo)
	assert.True(t, f720.HasAudio)
	assert.Equal(t, "avc1.4d401f", f720.VideoCodec)
	assert.Equal(t, "mp4a.40.2", f720.AudioCodec)
	assert.Equal(t, "https://cdn.example/stream_720.m3u8", f720.URL)
}

func TestSplitCodecsAudioFirst(t *testing.T) {
	v, a := splitCodecs("mp4a.40.2,avc1.64001f")
	assert.Equal(t, "avc1.64001f", v)
	assert.Equal(t, "mp4a.40.2", a)
}

func TestApplyFilesizeApprox(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "hls-480", TBR: 836.28, HasVideo: true},
		{FormatID: "hls-aac-q2", ABR: 128, HasAudio: true},
	}
	applyFilesizeApprox(formats, 100*time.Second)
	assert.Greater(t, formats[0].FilesizeApprox, int64(0))
	assert.Greater(t, formats[1].FilesizeApprox, int64(0))
	// ~836.28 kbps * 100s / 8 ≈ 10_453_500
	assert.InDelta(t, 10_453_500, float64(formats[0].FilesizeApprox), 1)
}

func TestExpandHLSFormatsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "https://www.dailymotion.com", r.Header.Get("Origin"))
		_, _ = w.Write([]byte(demuxedMaster))
	}))
	defer srv.Close()

	in := []extractor.Format{
		{FormatID: "http-720", URL: "https://cdn.example/v.mp4", HasVideo: true, HasAudio: true, Height: 720},
		{FormatID: "hls-auto", URL: srv.URL + "/master.m3u8", HasVideo: true, HasAudio: true},
	}
	out := expandHLSFormats(context.Background(), srv.Client(), in, 60*time.Second)
	ids := formatIDs(out)
	assert.Contains(t, ids, "http-720")
	assert.Contains(t, ids, "hls-480")
	assert.Contains(t, ids, "hls-380")
	assert.Contains(t, ids, "hls-aac-q1")
	assert.Contains(t, ids, "hls-aac-q2")
	assert.NotContains(t, ids, "hls-auto")
}

func TestExpandHLSFormatsFallbackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	in := []extractor.Format{
		{FormatID: "hls-auto", URL: srv.URL + "/master.m3u8", HasVideo: true, HasAudio: true},
	}
	out := expandHLSFormats(context.Background(), srv.Client(), in, 0)
	require.Len(t, out, 1)
	assert.Equal(t, "hls-auto", out[0].FormatID)
}

func formatIDs(formats []extractor.Format) []string {
	ids := make([]string, len(formats))
	for i, f := range formats {
		ids[i] = f.FormatID
	}
	return ids
}
