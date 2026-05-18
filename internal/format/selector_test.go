package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ytgo/internal/extractor"
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
