package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenamePartFileSuccess(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "vid.f136.mp4.part")
	final := filepath.Join(dir, "vid.f136.mp4")
	require.NoError(t, os.WriteFile(part, []byte("data"), 0o644))

	require.NoError(t, renamePartFile(part, final))
	_, err := os.Stat(part)
	require.True(t, os.IsNotExist(err))
	got, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Equal(t, []byte("data"), got)
}

func TestRenamePartFileIdempotentWhenFinalExists(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "vid.f136.mp4.part")
	final := filepath.Join(dir, "vid.f136.mp4")
	// Simulate: rename already happened (or concurrent process finished).
	require.NoError(t, os.WriteFile(final, []byte("complete"), 0o644))

	require.NoError(t, renamePartFile(part, final))
	got, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Equal(t, []byte("complete"), got)
}

func TestRenamePartFileMissingBoth(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "vid.f136.mp4.part")
	final := filepath.Join(dir, "vid.f136.mp4")

	err := renamePartFile(part, final)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rename part file")
}
