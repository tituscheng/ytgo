package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/config"
)

func TestFindExistingMedia(t *testing.T) {
	dir := t.TempDir()
	videoID := "dQw4w9WgXcQ"

	t.Run("match completed media", func(t *testing.T) {
		path := filepath.Join(dir, "Never Gonna Give You Up ["+videoID+"].mp4")
		require.NoError(t, os.WriteFile(path, []byte("video"), 0644))

		found, ok := findExistingMedia(dir, videoID)
		require.True(t, ok)
		assert.Equal(t, path, found)
	})

	t.Run("no match when directory empty", func(t *testing.T) {
		empty := t.TempDir()
		_, ok := findExistingMedia(empty, videoID)
		assert.False(t, ok)
	})

	t.Run("exclude part file", func(t *testing.T) {
		d := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(d, "Video ["+videoID+"].mp4.part"), []byte("partial"), 0644))
		_, ok := findExistingMedia(d, videoID)
		assert.False(t, ok)
	})

	t.Run("exclude intermediate format file", func(t *testing.T) {
		d := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(d, "Video ["+videoID+"].f137.mp4"), []byte("partial"), 0644))
		_, ok := findExistingMedia(d, videoID)
		assert.False(t, ok)
	})

	t.Run("exclude sidecar files", func(t *testing.T) {
		d := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(d, "Video ["+videoID+"].info.json"), []byte("{}"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(d, "Video ["+videoID+"].description"), []byte("desc"), 0644))
		_, ok := findExistingMedia(d, videoID)
		assert.False(t, ok)
	})

	t.Run("empty video ID", func(t *testing.T) {
		_, ok := findExistingMedia(dir, "")
		assert.False(t, ok)
	})

	t.Run("non-media extension ignored", func(t *testing.T) {
		d := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(d, "Video ["+videoID+"].txt"), []byte("nope"), 0644))
		_, ok := findExistingMedia(d, videoID)
		assert.False(t, ok)
	})
}

func TestOutputDir(t *testing.T) {
	assert.Equal(t, ".", outputDir(configWithPaths("")))
	assert.Equal(t, "/tmp/videos", outputDir(configWithPaths("/tmp/videos")))
}

func TestSkipIfExistingMediaDirect(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "My Video [test123].mp4")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0644))
	eng := NewEngine(config.DownloadOptions{
		SkipExisting:   true,
		Paths:          tmpDir,
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
	})
	assert.True(t, eng.skipIfExistingMedia("test123", "My Video"))
}

func configWithPaths(p string) config.DownloadOptions {
	return config.DownloadOptions{Paths: p}
}