package innertube

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeStreaming_PrefersVisionOSAdaptive(t *testing.T) {
	vr := &StreamingData{
		Formats: []Format{
			{ItagNo: 18, URL: "https://vr.example/18"},
		},
		AdaptiveFormats: []Format{
			{ItagNo: 137, URL: "https://vr.example/137"},
			{ItagNo: 140, URL: "https://vr.example/140"},
		},
	}
	vis := &StreamingData{
		AdaptiveFormats: []Format{
			{ItagNo: 399, URL: "https://vis.example/399"},
			{ItagNo: 251, URL: "https://vis.example/251"},
		},
	}

	got := mergeStreaming(vr, vis)
	require.Len(t, got.Formats, 1)
	assert.Equal(t, 18, got.Formats[0].ItagNo)
	assert.Equal(t, "https://vr.example/18", got.Formats[0].URL)

	var itags []int
	for _, f := range got.AdaptiveFormats {
		itags = append(itags, f.ItagNo)
	}
	assert.ElementsMatch(t, []int{399, 251}, itags)
}

func TestMergeStreaming_KeepsVRAdaptiveWhenVisionOSEmpty(t *testing.T) {
	vr := &StreamingData{
		Formats: []Format{{ItagNo: 18, URL: "https://vr.example/18"}},
		AdaptiveFormats: []Format{
			{ItagNo: 137, URL: "https://vr.example/137"},
			{ItagNo: 140, URL: "https://vr.example/140"},
		},
	}
	vis := &StreamingData{
		// SABR-only: cipher / no URL
		AdaptiveFormats: []Format{
			{ItagNo: 399, Cipher: "s=abc"},
		},
	}

	got := mergeStreaming(vr, vis)
	var itags []int
	for _, f := range got.AdaptiveFormats {
		itags = append(itags, f.ItagNo)
	}
	assert.ElementsMatch(t, []int{137, 140}, itags)
}

func TestMergeStreaming_KeepsDubbedAndOriginalItag(t *testing.T) {
	es := &AudioTrack{DisplayName: "Spanish", ID: "es.3"}
	en := &AudioTrack{DisplayName: "English original", ID: "en.4", AudioIsDefault: true}
	got := mergeStreaming(nil, &StreamingData{
		AdaptiveFormats: []Format{
			{ItagNo: 140, URL: "https://vis.example/140?xtags=acont%3Ddubbed%3Alang%3Des", AudioTrack: es},
			{ItagNo: 140, URL: "https://vis.example/140?xtags=acont%3Doriginal%3Alang%3Den", AudioTrack: en},
			{ItagNo: 135, URL: "https://vis.example/135"},
		},
	})
	require.Len(t, got.AdaptiveFormats, 3)
	var tracks []string
	for _, f := range got.AdaptiveFormats {
		if f.ItagNo != 140 || f.AudioTrack == nil {
			continue
		}
		tracks = append(tracks, f.AudioTrack.ID)
	}
	assert.ElementsMatch(t, []string{"es.3", "en.4"}, tracks)
}

func TestAudioVariantKey(t *testing.T) {
	assert.Equal(t, "140:en.4", audioVariantKey(Format{
		ItagNo:     140,
		URL:        "https://x/140?xtags=acont%3Doriginal%3Alang%3Den",
		AudioTrack: &AudioTrack{ID: "en.4"},
	}))
	assert.Equal(t, "140:acont=original:lang=en", audioVariantKey(Format{
		ItagNo: 140,
		URL:    "https://x/140?xtags=acont%3Doriginal%3Alang%3Den",
	}))
	assert.Equal(t, "18", audioVariantKey(Format{ItagNo: 18, URL: "https://x/18"}))
}

func TestMergeStreaming_DropsCipherAndEmptyURL(t *testing.T) {
	got := mergeStreaming(nil, &StreamingData{
		Formats: []Format{
			{ItagNo: 18, URL: "https://ok.example/18"},
			{ItagNo: 22, Cipher: "s=xyz"},
			{ItagNo: 17, URL: ""},
		},
	})
	require.Len(t, got.Formats, 1)
	assert.Equal(t, 18, got.Formats[0].ItagNo)
}

func TestMergeStreaming_DedupesItagPrefersQuality(t *testing.T) {
	got := mergeStreaming(
		&StreamingData{Formats: []Format{{ItagNo: 18, URL: "https://vr.example/18"}}},
		&StreamingData{Formats: []Format{{ItagNo: 18, URL: "https://vis.example/18"}}},
	)
	require.Len(t, got.Formats, 1)
	assert.Equal(t, "https://vis.example/18", got.Formats[0].URL)
}

func TestMergeStreaming_PrefersQualityHLS(t *testing.T) {
	got := mergeStreaming(
		&StreamingData{HlsManifestURL: "https://vr.example/master.m3u8"},
		&StreamingData{HlsManifestURL: "https://vis.example/master.m3u8"},
	)
	assert.Equal(t, "https://vis.example/master.m3u8", got.HlsManifestURL)
}

func TestUsableFormat(t *testing.T) {
	assert.True(t, usableFormat(Format{URL: "https://x"}))
	assert.False(t, usableFormat(Format{}))
	assert.False(t, usableFormat(Format{URL: "https://x", Cipher: "s=1"}))
}

func TestPlayabilityHelpers(t *testing.T) {
	assert.False(t, isPlayable(nil))
	assert.True(t, isPlayable(&PlayerResponse{PlayabilityStatus: PlayabilityStatus{Status: "OK"}}))
	assert.True(t, isPrivate(&PlayerResponse{PlayabilityStatus: PlayabilityStatus{
		Status: "LOGIN_REQUIRED", Reason: "This video is private",
	}}))
	assert.True(t, isAgeRestricted(&PlayerResponse{PlayabilityStatus: PlayabilityStatus{
		Status: "LOGIN_REQUIRED", Reason: "Sign in to confirm your age",
	}}))
	assert.True(t, isUnplayable(&PlayerResponse{PlayabilityStatus: PlayabilityStatus{Status: "UNPLAYABLE"}}))
	assert.True(t, isUnplayable(&PlayerResponse{PlayabilityStatus: PlayabilityStatus{Status: "ERROR"}}))
}
