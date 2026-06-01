package subtitle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

const validJSON3 = `{"events":[{"tStartMs":0,"dDurationMs":2000,"segs":[{"utf8":"Hello world"}]}]}`

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

func TestConvertRejectsXML(t *testing.T) {
	_, err := Convert([]byte("<root/>"), "srv3", "srt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestSegmentsText(t *testing.T) {
	assert.Equal(t, "Hello world", segmentsText([]json3Seg{{UTF8: "Hello "}, {UTF8: "world"}}))
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

func TestValidateTargetFormat(t *testing.T) {
	require.NoError(t, validateTargetFormat("srt"))
	require.NoError(t, validateTargetFormat("vtt"))
	require.Error(t, validateTargetFormat("ass"))
	require.Error(t, validateTargetFormat(""))
}

func TestForceJSON3SetsQuery(t *testing.T) {
	out := forceJSON3("https://www.youtube.com/api/timedtext?v=abc&lang=en")
	assert.Contains(t, out, "fmt=json3")
}

func TestForceJSON3Overrides(t *testing.T) {
	out := forceJSON3("https://x.test/path?fmt=srv3&lang=en")
	assert.Contains(t, out, "fmt=json3")
	assert.NotContains(t, out, "fmt=srv3")
}

// fastDownloader returns a Downloader with short backoff for tests.
func fastDownloader(client *http.Client) *Downloader {
	d := NewDownloader(client)
	d.BaseBackoff = time.Millisecond
	return d
}

func TestDownloadRetriesTransient(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "json3", r.URL.Query().Get("fmt"))
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(validJSON3))
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dest := filepath.Join(t.TempDir(), "out.srt")
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL, Ext: "json3"}, dest, "srt")
	require.NoError(t, err)
	require.FileExists(t, dest)
	assert.Equal(t, int32(3), atomic.LoadInt32(&hits))
}

func TestDownloadExhaustsRetriesAndLeavesNoTmp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.srt")
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.Error(t, err)
	assert.True(t, IsRetryable(err), "expected retryable: %v", err)
	require.NoFileExists(t, dest)
	require.NoFileExists(t, dest+".tmp")
}

func TestDownloadNonRetryableErrorReturnsImmediately(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dest := filepath.Join(t.TempDir(), "out.srt")
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.Error(t, err)
	assert.False(t, IsRetryable(err))
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestDownloadCreatesParentDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validJSON3))
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dest := filepath.Join(t.TempDir(), "nested", "sub", "out.srt")
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.NoError(t, err)
	require.FileExists(t, dest)
}

func TestDownloadAtomic_NoTmpOnConvertFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dest := filepath.Join(t.TempDir(), "out.srt")
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.Error(t, err)
	require.NoFileExists(t, dest)
	require.NoFileExists(t, dest+".tmp")
}

func TestDownloadContextCancellation(t *testing.T) {
	// Server is slow; cancellation should propagate before completion.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	d.MaxAttempts = 5

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	dest := filepath.Join(t.TempDir(), "out.srt")
	start := time.Now()
	err := d.Download(ctx, extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "cancellation should be prompt")
}

func TestDownloadHonorsRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(validJSON3))
	}))
	defer srv.Close()

	d := fastDownloader(srv.Client())
	dest := filepath.Join(t.TempDir(), "out.srt")
	start := time.Now()
	err := d.Download(context.Background(), extractor.Subtitle{URL: srv.URL}, dest, "srt")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
}

func TestSelectTrackPrefersManual(t *testing.T) {
	// pickBest direct test via selectTrack
	subs := map[string][]extractor.Subtitle{
		"en": {{URL: "manual"}},
	}
	auto := map[string][]extractor.Subtitle{
		"en": {{URL: "auto", AutoGenerated: true}},
	}
	track, isAuto, ok := selectTrack(fakeInfo(subs, auto), "en", true)
	require.True(t, ok)
	assert.False(t, isAuto)
	assert.Equal(t, "manual", track.URL)
}

func TestSelectTrackFallsBackToAuto(t *testing.T) {
	auto := map[string][]extractor.Subtitle{
		"en": {{URL: "auto", AutoGenerated: true}},
	}
	track, isAuto, ok := selectTrack(fakeInfo(nil, auto), "en", true)
	require.True(t, ok)
	assert.True(t, isAuto)
	assert.Equal(t, "auto", track.URL)
}

func TestSelectTrackSkipsAutoWhenDisabled(t *testing.T) {
	auto := map[string][]extractor.Subtitle{
		"en": {{URL: "auto", AutoGenerated: true}},
	}
	_, _, ok := selectTrack(fakeInfo(nil, auto), "en", false)
	assert.False(t, ok)
}

func TestSelectTrackRegionFallback(t *testing.T) {
	subs := map[string][]extractor.Subtitle{
		"en-US": {{URL: "us"}},
	}
	track, _, ok := selectTrack(fakeInfo(subs, nil), "en", false)
	require.True(t, ok)
	assert.Equal(t, "us", track.URL)
}

func TestSelectTrackPrefersNonAutoByName(t *testing.T) {
	subs := map[string][]extractor.Subtitle{
		"en": {
			{URL: "first", Name: "English (auto-generated)"},
			{URL: "second", Name: "English"},
		},
	}
	track, _, ok := selectTrack(fakeInfo(subs, nil), "en", false)
	require.True(t, ok)
	assert.Equal(t, "second", track.URL)
}

func TestParseRetryAfter(t *testing.T) {
	assert.Equal(t, 3*time.Second, parseRetryAfter("3"))
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, time.Duration(0), parseRetryAfter("garbage"))
}

func TestWriteSubsReportsMissingLang(t *testing.T) {
	info := fakeInfo(nil, nil)
	var reported []string
	var reportedErr error
	written, err := WriteSubs(context.Background(), info, WriteOptions{
		Langs:    []string{"en"},
		Format:   "srt",
		BasePath: t.TempDir(),
		BaseName: "video",
		OnError: func(lang string, ferr error, _ bool) {
			reported = append(reported, lang)
			reportedErr = ferr
		},
	})
	// A missing track is reported for visibility but must NOT fail the call,
	// so a video without captions still downloads cleanly.
	require.NoError(t, err)
	assert.Empty(t, written)
	assert.Equal(t, []string{"en"}, reported)
	assert.ErrorIs(t, reportedErr, ErrNoTrack)
}

func fakeInfo(subs, auto map[string][]extractor.Subtitle) *ytgo.VideoInfo {
	if subs == nil {
		subs = map[string][]extractor.Subtitle{}
	}
	if auto == nil {
		auto = map[string][]extractor.Subtitle{}
	}
	return &ytgo.VideoInfo{Subtitles: subs, AutoSubtitles: auto}
}
