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

func TestBuildOutputPath(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "abc", Title: "My Video", UploadDate: "20240115"}
	cfg := config.DownloadOptions{OutputTemplate: "%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s"}
	eng := NewEngine(cfg)
	path, err := eng.buildOutputPath(info, []ytgo.Format{{Ext: "mp4"}})
	require.NoError(t, err)
	assert.Equal(t, "2024-01-15 - My Video.mp4", path)
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
		OnProgress: func(down, tot int64) {
			progressCalled = true
			finalDown = down
			finalTot = tot
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

func TestProgressAggregate(t *testing.T) {
	var calls []struct{ down, tot int64 }
	pa := newProgressAggregate(func(down, tot int64) {
		calls = append(calls, struct{ down, tot int64 }{down, tot})
	})

	pa.report("v1", 100, 1000)
	require.Len(t, calls, 1)
	assert.Equal(t, int64(100), calls[0].down)
	assert.Equal(t, int64(1000), calls[0].tot)

	pa.report("a1", 200, 500)
	require.Len(t, calls, 2)
	assert.Equal(t, int64(300), calls[1].down)
	assert.Equal(t, int64(1500), calls[1].tot)

	// Update an existing format
	pa.report("v1", 500, 1000)
	require.Len(t, calls, 3)
	assert.Equal(t, int64(700), calls[2].down)
	assert.Equal(t, int64(1500), calls[2].tot)
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
