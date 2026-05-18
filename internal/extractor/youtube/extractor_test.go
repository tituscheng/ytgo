package youtube

import (
	"testing"

	"github.com/kkdai/youtube/v2"
	"github.com/stretchr/testify/assert"
)

func TestSuitable(t *testing.T) {
	e := NewExtractor(0)
	assert.True(t, e.Suitable("https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
	assert.True(t, e.Suitable("https://youtu.be/dQw4w9WgXcQ"))
	assert.True(t, e.Suitable("https://www.youtube.com/shorts/abcdefgH123"))
	assert.False(t, e.Suitable("https://example.com/video"))
}

func TestExtractVideoID(t *testing.T) {
	assert.Equal(t, "dQw4w9WgXcQ", extractVideoID("https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
	assert.Equal(t, "dQw4w9WgXcQ", extractVideoID("https://youtu.be/dQw4w9WgXcQ"))
	assert.Equal(t, "abcdefgH123", extractVideoID("https://www.youtube.com/shorts/abcdefgH123"))
	assert.Equal(t, "", extractVideoID("https://example.com"))
}

func TestExtractPlaylistID(t *testing.T) {
	assert.Equal(t, "PLabc123", extractPlaylistID("https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLabc123"))
	assert.Equal(t, "", extractPlaylistID("https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
}

func TestNormalizeLang(t *testing.T) {
	assert.Equal(t, "en", normalizeLang("en"))
	assert.Equal(t, "en", normalizeLang("en-US"))
	assert.Equal(t, "ja", normalizeLang("ja"))
}

func TestMapFormat(t *testing.T) {
	f := youtube.Format{
		ItagNo:        22,
		URL:           "https://example.com/video.mp4",
		MimeType:      `video/mp4; codecs="avc1.64001F, mp4a.40.2"`,
		Bitrate:       1000000,
		Width:         1280,
		Height:        720,
		FPS:           30,
		Quality:       "hd720",
		QualityLabel:  "720p",
		ContentLength: 12345678,
		AudioChannels: 2,
	}
	format := mapFormat(f)
	assert.Equal(t, "22", format.FormatID)
	assert.Equal(t, "mp4", format.Ext)
	assert.Equal(t, 1280, format.Width)
	assert.Equal(t, 720, format.Height)
	assert.Equal(t, 30.0, format.FPS)
	assert.True(t, format.HasVideo)
	assert.True(t, format.HasAudio)
	assert.Equal(t, "avc1.64001F", format.VideoCodec)
	assert.Equal(t, "mp4a.40.2", format.AudioCodec)
	assert.Equal(t, int64(12345678), format.Filesize)
}

func TestParseMimeType(t *testing.T) {
	ext, v, a := parseMimeType(`video/webm; codecs="vp9"`)
	assert.Equal(t, "webm", ext)
	assert.Equal(t, "vp9", v)
	assert.Equal(t, "", a)

	ext, v, a = parseMimeType(`video/mp4; codecs="avc1.64001F, mp4a.40.2"`)
	assert.Equal(t, "mp4", ext)
	assert.Equal(t, "avc1.64001F", v)
	assert.Equal(t, "mp4a.40.2", a)
}
