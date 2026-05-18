package archive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveOpenNew(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "archive.txt")
	a, err := Open(path)
	require.NoError(t, err)
	assert.False(t, a.Has("abc123"))
}

func TestArchiveOpenExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "archive.txt")
	require.NoError(t, os.WriteFile(path, []byte("abc123\ndef456\n"), 0644))

	a, err := Open(path)
	require.NoError(t, err)
	assert.True(t, a.Has("abc123"))
	assert.True(t, a.Has("def456"))
	assert.False(t, a.Has("xyz789"))
}

func TestArchiveAdd(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "archive.txt")
	a, err := Open(path)
	require.NoError(t, err)

	require.NoError(t, a.Add("abc123"))
	assert.True(t, a.Has("abc123"))

	// Adding again should not duplicate
	require.NoError(t, a.Add("abc123"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	assert.Equal(t, 1, lines)
}

func TestArchiveNoPath(t *testing.T) {
	a, err := Open("")
	require.NoError(t, err)
	assert.False(t, a.Has("anything"))
	assert.NoError(t, a.Add("anything"))
}
