package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/config"
	"github.com/tituscheng/ytgo/internal/downloader"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

// mockExtractor is a test extractor that returns canned data.
type mockExtractor struct {
	info *ytgo.VideoInfo
}

func (m *mockExtractor) Name() string             { return "mock" }
func (m *mockExtractor) Suitable(url string) bool { return true }
func (m *mockExtractor) Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error) {
	return m.info, nil
}

func TestEngineRun_ListFormats(t *testing.T) {
	info := &ytgo.VideoInfo{
		ID: "test123",
		Formats: []ytgo.Format{
			{FormatID: "1", Height: 720, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	eng := NewEngine(config.DownloadOptions{ListFormats: true})
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)
}

func TestEngineRun_SkipDownload(t *testing.T) {
	tmpDir := t.TempDir()
	info := &ytgo.VideoInfo{
		ID:          "test123",
		Title:       "My Video",
		Description: "A great video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: "http://example.com/video.mp4", Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		SkipDownload:     true,
		WriteInfoJSON:    true,
		WriteDescription: true,
		OutputTemplate:   "%(title)s [%(id)s].%(ext)s",
		Paths:            tmpDir,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "My Video [test123].info.json"))
	assert.FileExists(t, filepath.Join(tmpDir, "My Video [test123].description"))
}

func TestEngineRun_Download(t *testing.T) {
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	info := &ytgo.VideoInfo{
		ID:    "test123",
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(tmpDir, "My Video [test123].mp4"))
}

func TestEngineRun_Archive(t *testing.T) {
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "archive.txt")
	info := &ytgo.VideoInfo{
		ID:    "test123",
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		OutputTemplate:  "%(title)s [%(id)s].%(ext)s",
		Paths:           tmpDir,
		NoProgress:      true,
		DownloadArchive: archivePath,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	// Second run should be skipped
	_, err = eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	data, err := os.ReadFile(archivePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "test123")
}

func TestEngineRun_SkipExisting(t *testing.T) {
	tmpDir := t.TempDir()
	videoID := "abc12345678"
	outPath := filepath.Join(tmpDir, "My Video ["+videoID+"].mp4")
	require.NoError(t, os.WriteFile(outPath, []byte("existing"), 0644))

	var requestCount int
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Write(content)
	}))
	defer srv.Close()

	info := &ytgo.VideoInfo{
		ID:    videoID,
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
		SkipExisting:   true,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	videoURL := "https://www.youtube.com/watch?v=" + videoID
	_, err := eng.Run(context.Background(), videoURL)
	require.NoError(t, err)
	assert.Equal(t, 0, requestCount)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("existing"), data)
}

func TestEngineRun_SkipExisting_AfterDownload(t *testing.T) {
	var requestCount int
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	videoID := "abc12345678"
	info := &ytgo.VideoInfo{
		ID:    videoID,
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
		SkipExisting:   true,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	videoURL := "https://www.youtube.com/watch?v=" + videoID
	_, err := eng.Run(context.Background(), videoURL)
	require.NoError(t, err)

	before := requestCount
	_, err = eng.Run(context.Background(), videoURL)
	require.NoError(t, err)
	assert.Equal(t, before, requestCount)
}

func TestEngineRun_SkipExisting_ForceRedownload(t *testing.T) {
	var requestCount int
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	videoID := "abc12345678"
	info := &ytgo.VideoInfo{
		ID:    videoID,
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
		SkipExisting:   false,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	videoURL := "https://www.youtube.com/watch?v=" + videoID
	_, err := eng.Run(context.Background(), videoURL)
	require.NoError(t, err)

	before := requestCount
	_, err = eng.Run(context.Background(), videoURL)
	require.NoError(t, err)
	assert.Greater(t, requestCount, before)
}

func TestBuildOutputPath(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "abc", Title: "My Video", UploadDate: "20240115"}
	cfg := config.DownloadOptions{OutputTemplate: "%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s"}
	eng := NewEngine(cfg)
	path, err := eng.buildOutputPath(info, []ytgo.Format{{Ext: "mp4"}})
	require.NoError(t, err)
	assert.Equal(t, "2024-01-15 - My Video.mp4", path)
}

func TestBuildOutputPath_ExtractAudioSingleFormat(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "abc", Title: "My Video"}
	eng := NewEngine(config.DownloadOptions{ExtractAudio: true, AudioFormat: "m4a"})
	path, err := eng.buildOutputPath(info, []ytgo.Format{{Ext: "m4a", HasAudio: true}})
	require.NoError(t, err)
	assert.Equal(t, "My Video [abc].m4a", path)
}

func TestBuildOutputPath_ExtractAudioMultiFormat(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "abc", Title: "My Video"}
	eng := NewEngine(config.DownloadOptions{ExtractAudio: true, AudioFormat: "m4a"})
	path, err := eng.buildOutputPath(info, []ytgo.Format{
		{Ext: "webm", VideoCodec: "vp9", HasVideo: true},
		{Ext: "m4a", AudioCodec: "mp4a", HasAudio: true},
	})
	require.NoError(t, err)
	assert.Equal(t, "My Video [abc].mkv", path)
}

func TestEngineRun_DownloadWithProgress(t *testing.T) {
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	info := &ytgo.VideoInfo{
		ID:    "test123",
		Title: "My Video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true, Filesize: int64(len(content))},
		},
	}

	var progressCalled bool
	var finalDown, finalTot int64
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
		OnProgress: func(p ytgo.Progress) {
			progressCalled = true
			finalDown = p.Cur
			finalTot = p.Tot
		},
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	assert.True(t, progressCalled, "OnProgress should have been called")
	assert.Equal(t, int64(len(content)), finalDown, "downloaded bytes should match content size")
	assert.Equal(t, int64(len(content)), finalTot, "total bytes should match content size")
}

func TestEngineRun_EnrichMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	info := &ytgo.VideoInfo{
		ID:          "test123",
		Title:       "My Video",
		LikeCount:   42000,
		Description: "A great video",
		Formats: []ytgo.Format{
			{FormatID: "1", URL: "http://example.com/video.mp4", Ext: "mp4", HasVideo: true, HasAudio: true},
		},
	}
	cfg := config.DownloadOptions{
		SkipDownload:   true,
		WriteInfoJSON:  true,
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Paths:          tmpDir,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, "My Video [test123].info.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "\"like_count\": 42000")
}

func TestHumanSize(t *testing.T) {
	assert.Equal(t, "unknown", humanSize(0))
	assert.Equal(t, "500 B", humanSize(500))
	assert.Equal(t, "1.0 KB", humanSize(1024))
	assert.Equal(t, "1.0 MB", humanSize(1024*1024))
	assert.Equal(t, "1.0 GB", humanSize(1024*1024*1024))
}

// --- OnError callback tests ---

// mockFailingExtractor always returns an error.
type mockFailingExtractor struct{ err error }

func (m *mockFailingExtractor) Name() string             { return "mock-fail" }
func (m *mockFailingExtractor) Suitable(url string) bool { return true }
func (m *mockFailingExtractor) Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error) {
	return nil, m.err
}

func TestEngineRun_OnError_SingleVideo(t *testing.T) {
	var failure ytgo.DownloadFailure
	cfg := config.DownloadOptions{
		OnError: func(f ytgo.DownloadFailure) {
			failure = f
		},
	}
	eng := NewEngine(cfg)
	eng.Register(&mockFailingExtractor{err: assert.AnError})

	_, err := eng.Run(context.Background(), "http://example.com")
	require.Error(t, err)

	assert.Equal(t, "http://example.com", failure.URL)
	assert.Equal(t, "extract", failure.Stage)
	assert.Equal(t, assert.AnError.Error(), failure.Error)
	assert.False(t, failure.Retryable)
}

func TestEngineRun_OnError_Playlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var failures []ytgo.DownloadFailure
	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s.%(ext)s",
		Paths:          t.TempDir(),
		NoProgress:     true,
		NoWarnings:     true,
		OnError: func(f ytgo.DownloadFailure) {
			failures = append(failures, f)
		},
	}

	info := &ytgo.VideoInfo{
		ID:            "pl123",
		Title:         "My Playlist",
		PlaylistTitle: "My Playlist",
		Entries: []*ytgo.VideoInfo{
			{ID: "v1", Title: "Video 1", OriginalURL: "http://example.com/1", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
			{ID: "v2", Title: "Video 2", OriginalURL: "http://example.com/2", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
			{ID: "v3", Title: "Video 3", OriginalURL: "http://example.com/3", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
		},
	}

	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	// Playlist should return nil even though all 3 videos failed
	_, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	require.Len(t, failures, 3)
	byID := make(map[string]ytgo.DownloadFailure)
	for _, f := range failures {
		byID[f.VideoID] = f
	}
	for i := 1; i <= 3; i++ {
		f, ok := byID[fmt.Sprintf("v%d", i)]
		require.True(t, ok, "expected failure for v%d", i)
		assert.Equal(t, "download", f.Stage)
		assert.True(t, f.Retryable, "HTTP 503 should be retryable")
	}
}

func TestAllowsQualityLadderFallback(t *testing.T) {
	assert.True(t, allowsQualityLadderFallback(""))
	assert.True(t, allowsQualityLadderFallback("best"))
	assert.True(t, allowsQualityLadderFallback("best[height<=720]"))
	assert.True(t, allowsQualityLadderFallback("bv*+ba/best"))
	assert.False(t, allowsQualityLadderFallback("hls-480"))
	assert.False(t, allowsQualityLadderFallback("http-720"))
}

func TestLowerMuxedHLSFormats(t *testing.T) {
	primary := ytgo.Format{FormatID: "hls-480", Height: 848, TBR: 836, HasVideo: true, HasAudio: true}
	all := []ytgo.Format{
		primary,
		{FormatID: "hls-720", Height: 1280, TBR: 2000, HasVideo: true, HasAudio: true},
		{FormatID: "hls-380", Height: 640, TBR: 460, HasVideo: true, HasAudio: true},
		{FormatID: "hls-aac-q2", HasVideo: false, HasAudio: true},
		{FormatID: "http-480", Height: 480, HasVideo: true, HasAudio: true},
	}
	alts := lowerMuxedHLSFormats(primary, all)
	require.Len(t, alts, 1)
	assert.Equal(t, "hls-380", alts[0].FormatID)
}

// --- Error classification tests ---

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil", nil, false},
		{"HTTP 429", fmt.Errorf("HTTP 429"), true},
		{"HTTP 503", fmt.Errorf("HTTP 503"), true},
		{"HTTP 504", fmt.Errorf("HTTP 504"), true},
		{"HTTP 404", fmt.Errorf("HTTP 404"), false},
		{"HTTP 403", fmt.Errorf("HTTP 403"), false},
		{"connection reset", fmt.Errorf("connection reset by peer"), true},
		{"timeout", fmt.Errorf("request timeout"), true},
		{"no such host", fmt.Errorf("no such host"), true},
		{"private video", fmt.Errorf("video is private"), false},
		{"generic", fmt.Errorf("something went wrong"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.retryable, isRetryable(tt.err))
		})
	}
}

// --- JSON serialization test ---

func TestDownloadFailure_JSON(t *testing.T) {
	f := ytgo.DownloadFailure{
		VideoID:   "abc123",
		Title:     "Test Video",
		URL:       "http://example.com",
		FormatID:  "22",
		Stage:     "download",
		Error:     "HTTP 503",
		Retryable: true,
	}
	data, err := json.Marshal(f)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"video_id":"abc123"`)
	assert.Contains(t, string(data), `"stage":"download"`)
	assert.Contains(t, string(data), `"retryable":true`)
}

func TestIsForbiddenTyped(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"typed 403", &downloader.StatusError{StatusCode: 403}, true},
		{"typed 404", &downloader.StatusError{StatusCode: 404}, false},
		{"wrapped 403", fmt.Errorf("wrapped: %w", &downloader.StatusError{StatusCode: 403}), true},
		{"string 403", fmt.Errorf("HTTP 403"), true},
		{"string 404", fmt.Errorf("HTTP 404"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isForbidden(tt.err))
		})
	}
}

func TestIsRetryableNetwork(t *testing.T) {
	// Test that url.Error with Temporary/Timeout is recognized
	timeoutErr := &url.Error{Op: "Get", URL: "http://example.com", Err: context.DeadlineExceeded}
	assert.True(t, isRetryable(timeoutErr), "timeout url.Error should be retryable")

	// Test typed errors
	assert.True(t, isRetryable(&downloader.StatusError{StatusCode: 429}), "typed 429 should be retryable")
	assert.True(t, isRetryable(&downloader.StatusError{StatusCode: 503}), "typed 503 should be retryable")
	assert.False(t, isRetryable(&downloader.StatusError{StatusCode: 403}), "typed 403 should not be retryable")
}

func TestPlaylistReport(t *testing.T) {
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	info := &ytgo.VideoInfo{
		ID:            "pl123",
		Title:         "My Playlist",
		PlaylistTitle: "My Playlist",
		Entries: []*ytgo.VideoInfo{
			{ID: "v1", Title: "Video 1", OriginalURL: "http://example.com/1", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
			{ID: "v2", Title: "Video 2", OriginalURL: "http://example.com/2", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
			{ID: "v3", Title: "Video 3", OriginalURL: "http://example.com/3", Formats: []ytgo.Format{
				{FormatID: "1", URL: srv.URL, Ext: "mp4", HasVideo: true, HasAudio: true},
			}},
		},
	}

	cfg := config.DownloadOptions{
		OutputTemplate: "%(title)s.%(ext)s",
		Paths:          tmpDir,
		NoProgress:     true,
		NoWarnings:     true,
	}
	eng := NewEngine(cfg)
	eng.Register(&mockExtractor{info: info})

	report, err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, 3, report.Total)
	assert.Equal(t, 0, len(report.Failed))
	assert.Equal(t, 0, report.Skipped)
	assert.Equal(t, 3, report.Succeeded)
}

func TestResolveDownloadURL(t *testing.T) {
	hlsURL := "https://manifest.googlevideo.com/hls/playlist.m3u8"
	dashURL := "https://manifest.googlevideo.com/dash/playlist.mpd"
	liveDirect := "https://rr3---sn.example.googlevideo.com/videoplayback?live=1&itag=137"
	vodDirect := "https://rr3---sn.example.googlevideo.com/videoplayback?itag=137&clen=1000"

	infoWithHLS := &ytgo.VideoInfo{
		IsLiveContent: true,
		Formats: []ytgo.Format{
			{FormatID: "hls", URL: hlsURL},
			{FormatID: "137", URL: liveDirect, HasVideo: true, Filesize: 1000},
		},
	}
	infoWithDASH := &ytgo.VideoInfo{
		IsLiveContent: true,
		Formats: []ytgo.Format{
			{FormatID: "dash", URL: dashURL},
		},
	}

	tests := []struct {
		name      string
		info      *ytgo.VideoInfo
		format    ytgo.Format
		wantURL   string
		wantFFmpeg bool
	}{
		{
			name:       "manifest url",
			info:       infoWithHLS,
			format:     ytgo.Format{URL: hlsURL},
			wantURL:    hlsURL,
			wantFFmpeg: true,
		},
		{
			name:       "live=1 prefers hls manifest",
			info:       infoWithHLS,
			format:     ytgo.Format{URL: liveDirect, HasVideo: true},
			wantURL:    hlsURL,
			wantFFmpeg: true,
		},
		{
			name:       "live=1 without manifest uses direct url",
			info:       &ytgo.VideoInfo{IsLiveContent: true},
			format:     ytgo.Format{URL: liveDirect},
			wantURL:    liveDirect,
			wantFFmpeg: true,
		},
		{
			name:       "zero-size live video prefers hls",
			info:       infoWithHLS,
			format:     ytgo.Format{URL: "https://example.com/no-live-param", HasVideo: true, Filesize: 0},
			wantURL:    hlsURL,
			wantFFmpeg: true,
		},
		{
			name:       "zero-size live video falls back to dash",
			info:       infoWithDASH,
			format:     ytgo.Format{URL: "https://example.com/no-live-param", HasVideo: true, Filesize: 0},
			wantURL:    dashURL,
			wantFFmpeg: true,
		},
		{
			name:       "vod direct url uses segment downloader",
			info:       &ytgo.VideoInfo{IsLiveContent: false},
			format:     ytgo.Format{URL: vodDirect, HasVideo: true, Filesize: 1000},
			wantURL:    vodDirect,
			wantFFmpeg: false,
		},
		{
			name:       "live replay with known filesize uses segment downloader",
			info:       infoWithHLS,
			format:     ytgo.Format{URL: vodDirect, HasVideo: true, Filesize: 1000},
			wantURL:    vodDirect,
			wantFFmpeg: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, viaFFmpeg := resolveDownloadURL(tc.info, tc.format)
			assert.Equal(t, tc.wantURL, url)
			assert.Equal(t, tc.wantFFmpeg, viaFFmpeg)
		})
	}
}

func TestManifestFormat(t *testing.T) {
	info := &ytgo.VideoInfo{
		Formats: []ytgo.Format{
			{FormatID: "137", URL: "https://example.com/direct"},
			{FormatID: "dash", URL: "https://example.com/dash.mpd"},
			{FormatID: "hls", URL: "https://example.com/hls.m3u8"},
		},
	}
	require.NotNil(t, manifestFormat(info))
	assert.Equal(t, "hls", manifestFormat(info).FormatID)

	info.Formats = info.Formats[:2]
	require.NotNil(t, manifestFormat(info))
	assert.Equal(t, "dash", manifestFormat(info).FormatID)

	info.Formats = info.Formats[:1]
	assert.Nil(t, manifestFormat(info))
}

func TestAllFormatsLiveOrigin(t *testing.T) {
	assert.False(t, allFormatsLiveOrigin(nil))
	assert.False(t, allFormatsLiveOrigin([]ytgo.Format{
		{URL: "https://example.com/videoplayback?live=1"},
		{URL: "https://example.com/videoplayback?itag=140"},
	}))
	assert.True(t, allFormatsLiveOrigin([]ytgo.Format{
		{URL: "https://example.com/videoplayback?live=1"},
		{URL: "https://example.com/playlist.m3u8"},
	}))
}
