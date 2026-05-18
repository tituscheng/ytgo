package subtitle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertJSON3ToSRT(t *testing.T) {
	json3 := []byte(`{
		"events": [
			{"tStartMs": 0, "dDurationMs": 2000, "segs": [{"utf8": "Hello world"}]},
			{"tStartMs": 3000, "dDurationMs": 2500, "segs": [{"utf8": "Second line"}]}
		]
	}`)

	srt, err := Convert(json3, "json3", "srt")
	require.NoError(t, err)
	assert.Contains(t, srt, "1\n00:00:00,000 --> 00:00:02,000\nHello world\n")
	assert.Contains(t, srt, "2\n00:00:03,000 --> 00:00:05,500\nSecond line\n")
}

func TestConvertJSON3ToVTT(t *testing.T) {
	json3 := []byte(`{
		"events": [
			{"tStartMs": 1000, "dDurationMs": 2000, "segs": [{"utf8": "Hello"}]}
		]
	}`)

	vtt, err := Convert(json3, "json3", "vtt")
	require.NoError(t, err)
	assert.Contains(t, vtt, "WEBVTT")
	assert.Contains(t, vtt, "00:00:01.000 --> 00:00:03.000\nHello\n")
}

func TestSegmentsText(t *testing.T) {
	segs := []json3Seg{
		{UTF8: "Hello "},
		{UTF8: "world"},
	}
	assert.Equal(t, "Hello world", segmentsText(segs))
}

func TestMsToSRTTime(t *testing.T) {
	assert.Equal(t, "00:00:01,500", msToSRTTime(1500))
	assert.Equal(t, "00:01:02,000", msToSRTTime(62000))
	assert.Equal(t, "01:00:00,000", msToSRTTime(3600000))
}

func TestMsToVTTTime(t *testing.T) {
	assert.Equal(t, "00:00:01.500", msToVTTTime(1500))
	assert.Equal(t, "00:01:02.000", msToVTTTime(62000))
}
