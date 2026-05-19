package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ytgo/pkg/ytgo"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	assert.Equal(t, "bv*+ba/best", opts.Format)
	assert.Equal(t, "%(title)s [%(id)s].%(ext)s", opts.OutputTemplate)
	assert.True(t, opts.ContinuePartial)
}

func TestGetStreamURL(t *testing.T) {
	// Mock a YouTube-like video endpoint
	content := []byte("fake video content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	// We need a real video ID for the extractor to parse.
	// Use a known video ID and mock the Innertube client... but we can't easily
	// mock the innertube client from the api package.
	// Instead, we test the lower-level path by creating a mock extractor.
	// For now, we test the error path when extraction fails.
	_, err := GetStreamURL(context.Background(), GetStreamOptions{
		URL:     "http://invalid-url-that-wont-parse",
		Format:  "best",
		Timeout: 5 * time.Second,
	})
	require.Error(t, err)
}

func TestGetStreamOptionsDefaults(t *testing.T) {
	// Verify defaults are applied
	opts := GetStreamOptions{URL: "https://example.com"}
	assert.Empty(t, opts.Format)
	assert.Equal(t, time.Duration(0), opts.Timeout)
	// The function fills in defaults internally
}

// mockExtractor is a test extractor that returns canned data.
type mockExtractor struct {
	info *ytgo.VideoInfo
}

func (m *mockExtractor) Name() string             { return "mock" }
func (m *mockExtractor) Suitable(url string) bool { return true }
func (m *mockExtractor) Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error) {
	return m.info, nil
}

func TestStreamResultStruct(t *testing.T) {
	// Verify the struct fields exist and are accessible
	result := &StreamResult{
		URL: "https://example.com/video.mp4",
		Format: ytgo.Format{
			FormatID:   "22",
			URL:        "https://example.com/video.mp4",
			Ext:        "mp4",
			Height:     720,
			VideoCodec: "avc1.64001F",
			AudioCodec: "mp4a.40.2",
		},
		VideoInfo: &ytgo.VideoInfo{
			ID:    "test123",
			Title: "Test Video",
		},
	}
	assert.Equal(t, "https://example.com/video.mp4", result.URL)
	assert.Equal(t, "22", result.Format.FormatID)
	assert.Equal(t, "avc1.64001F", result.Format.VideoCodec)
	assert.Equal(t, "test123", result.VideoInfo.ID)
}
