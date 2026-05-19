package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ytgo/internal/config"
	"ytgo/pkg/ytgo"
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

	err := eng.Run(context.Background(), "http://example.com")
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

	err := eng.Run(context.Background(), "http://example.com")
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

	err := eng.Run(context.Background(), "http://example.com")
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

	err := eng.Run(context.Background(), "http://example.com")
	require.NoError(t, err)

	// Second run should be skipped
	err = eng.Run(context.Background(), "http://example.com")
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

	err := eng.Run(context.Background(), "http://example.com")
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

	err := eng.Run(context.Background(), "http://example.com")
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
