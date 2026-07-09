package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
)

func TestSelectBest(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true, TBR: 1000},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false, TBR: 2000},
		{FormatID: "3", Height: 360, HasVideo: true, HasAudio: true, TBR: 500},
	}
	result, err := Select("best", formats)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "2", result[0].FormatID) // 1080p video-only scores highest
}

func TestSelectWorst(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false},
	}
	result, err := Select("worst", formats)
	require.NoError(t, err)
	assert.Equal(t, "1", result[0].FormatID)
}

func TestSelectBestVideo(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false},
		{FormatID: "3", Height: 0, HasVideo: false, HasAudio: true},
	}
	result, err := Select("bv", formats)
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectBestAudio(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false},
		{FormatID: "3", ABR: 128, HasVideo: false, HasAudio: true},
	}
	result, err := Select("ba", formats)
	require.NoError(t, err)
	assert.Equal(t, "3", result[0].FormatID)
}

func TestSelectCombination(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false},
		{FormatID: "3", ABR: 128, HasVideo: false, HasAudio: true},
	}
	result, err := Select("bv+ba", formats)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "2", result[0].FormatID)
	assert.Equal(t, "3", result[1].FormatID)
}

func TestSelectFallback(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
	}
	result, err := Select("bv+ba/best", formats)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "1", result[0].FormatID)
}

func TestSelectFilter(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false},
		{FormatID: "3", Height: 480, HasVideo: true, HasAudio: true},
	}
	result, err := Select("best[height<=720]", formats)
	require.NoError(t, err)
	assert.Equal(t, "1", result[0].FormatID)
}

func TestSelectFormatCode(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "22", Height: 720},
		{FormatID: "18", Height: 360},
	}
	result, err := Select("18", formats)
	require.NoError(t, err)
	assert.Equal(t, "18", result[0].FormatID)
}

func TestSelectExtension(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Ext: "mp4", Height: 720},
		{FormatID: "2", Ext: "webm", Height: 1080},
	}
	result, err := Select("mp4", formats)
	require.NoError(t, err)
	assert.Equal(t, "1", result[0].FormatID)
}

func TestSelectEmptyFormats(t *testing.T) {
	_, err := Select("best", nil)
	assert.Error(t, err)
}

func TestSelectWithPrefs_VideoCodec(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 1080, VideoCodec: "vp9", Ext: "webm", HasVideo: true},
		{FormatID: "2", Height: 720, VideoCodec: "avc1.640028", Ext: "mp4", HasVideo: true},
	}
	// Without prefs, 1080p vp9 wins (higher height)
	result, err := Select("best", formats)
	require.NoError(t, err)
	assert.Equal(t, "1", result[0].FormatID)

	// With avc1 preference, 720p avc1 wins despite lower resolution
	result, err = SelectWithOptions("best", formats, SelectOptions{
		Preferences: Preferences{PreferVideoCodec: "avc1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectWithPrefs_AudioCodec(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", ABR: 160, AudioCodec: "opus", Ext: "webm", HasAudio: true},
		{FormatID: "2", ABR: 128, AudioCodec: "mp4a.40.2", Ext: "m4a", HasAudio: true},
	}
	// Without prefs, higher ABR wins
	result, err := Select("ba", formats)
	require.NoError(t, err)
	assert.Equal(t, "1", result[0].FormatID)

	// With mp4a preference, lower-bitrate AAC wins
	result, err = SelectWithOptions("ba", formats, SelectOptions{
		Preferences: Preferences{PreferAudioCodec: "mp4a"},
	})
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectWithPrefs_Container(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 1080, Ext: "webm", HasVideo: true},
		{FormatID: "2", Height: 720, Ext: "mp4", HasVideo: true},
	}
	result, err := SelectWithOptions("best", formats, SelectOptions{
		Preferences: Preferences{PreferContainer: "mp4"},
	})
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectWithPrefs_Combined(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 1080, VideoCodec: "vp9", AudioCodec: "opus", Ext: "webm", HasVideo: true, HasAudio: true},
		{FormatID: "2", Height: 720, VideoCodec: "avc1.640028", AudioCodec: "mp4a.40.2", Ext: "mp4", HasVideo: true, HasAudio: true},
	}
	result, err := SelectWithOptions("best", formats, SelectOptions{
		Preferences: Preferences{
			PreferVideoCodec: "avc1",
			PreferAudioCodec: "mp4a",
			PreferContainer:  "mp4",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectWithOptions_FilterFunc(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 1080, Ext: "webm", HasVideo: true},
		{FormatID: "2", Height: 720, Ext: "mp4", HasVideo: true},
		{FormatID: "3", Height: 480, Ext: "mp4", HasVideo: true},
	}
	result, err := SelectWithOptions("best", formats, SelectOptions{
		FormatFilter: func(f extractor.Format) bool {
			return f.Ext == "mp4"
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectWithOptions_FilterFuncEmptyResult(t *testing.T) {
	formats := []extractor.Format{
		{FormatID: "1", Height: 1080, Ext: "webm", HasVideo: true},
	}
	_, err := SelectWithOptions("best", formats, SelectOptions{
		FormatFilter: func(f extractor.Format) bool {
			return f.Ext == "mp4"
		},
	})
	assert.Error(t, err)
}

func TestSelectBackwardCompat(t *testing.T) {
	// Ensure Select() without options still behaves identically
	formats := []extractor.Format{
		{FormatID: "1", Height: 720, HasVideo: true, HasAudio: true, TBR: 1000},
		{FormatID: "2", Height: 1080, HasVideo: true, HasAudio: false, TBR: 2000},
		{FormatID: "3", Height: 360, HasVideo: true, HasAudio: true, TBR: 500},
	}
	result, err := Select("best", formats)
	require.NoError(t, err)
	assert.Equal(t, "2", result[0].FormatID)
}

func TestSelectBestDemuxedOnly(t *testing.T) {
	// Dailymotion-style demuxed HLS: no combined A/V format.
	formats := []extractor.Format{
		{FormatID: "hls-380", Height: 640, HasVideo: true, HasAudio: false, TBR: 460},
		{FormatID: "hls-480", Height: 848, HasVideo: true, HasAudio: false, TBR: 836},
		{FormatID: "hls-aac-q1", ABR: 64, HasVideo: false, HasAudio: true},
		{FormatID: "hls-aac-q2", ABR: 128, HasVideo: false, HasAudio: true},
	}
	result, err := Select("best", formats)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "hls-480", result[0].FormatID)
	assert.Equal(t, "hls-aac-q2", result[1].FormatID)

	// Default selector path.
	result, err = Select("bv*+ba/best", formats)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "hls-480", result[0].FormatID)
	assert.Equal(t, "hls-aac-q2", result[1].FormatID)
}
