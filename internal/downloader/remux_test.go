package downloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMPEGTSFile(t *testing.T) {
	dir := t.TempDir()
	tsPath := filepath.Join(dir, "a.ts")
	mp4Path := filepath.Join(dir, "a.mp4")
	require.NoError(t, os.WriteFile(tsPath, []byte{0x47, 0x40, 0x11, 0x10}, 0o644))
	// Minimal ftyp-like start (not 0x47).
	require.NoError(t, os.WriteFile(mp4Path, []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}, 0o644))

	assert.True(t, IsMPEGTSFile(tsPath))
	assert.False(t, IsMPEGTSFile(mp4Path))
	assert.False(t, IsMPEGTSFile(filepath.Join(dir, "missing")))
}

func TestWantsMP4Container(t *testing.T) {
	assert.True(t, wantsMP4Container("vid.mp4"))
	assert.True(t, wantsMP4Container("vid.mp4.part"))
	assert.True(t, wantsMP4Container("/tmp/x.m4a.part"))
	assert.False(t, wantsMP4Container("vid.ts"))
	assert.False(t, wantsMP4Container("vid.mkv"))
}

func TestRemuxMPEGTSToMP4_NoopWhenNotTS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.mp4")
	require.NoError(t, os.WriteFile(path, []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}, 0o644))
	require.NoError(t, RemuxMPEGTSToMP4(context.Background(), "", path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, byte('f'), data[4])
}

func TestRemuxMPEGTSToMP4_WithMockFFmpeg(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	// Fake ffmpeg: copy -i input to last arg as "mp4" (starts with ftyp).
	body := `#!/bin/sh
dest=""
src=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then src="$a"; fi
  dest="$a"
  prev="$a"
done
# Write a fake MP4 header so IsMPEGTSFile is false afterwards.
printf '\x00\x00\x00\x18ftypisom\x00\x00\x00\x00' > "$dest"
exit 0
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	path := filepath.Join(dir, "out.mp4")
	require.NoError(t, os.WriteFile(path, []byte{0x47, 0x00, 0x00, 0x00, 0x01}, 0o644))
	require.True(t, IsMPEGTSFile(path))

	err := RemuxMPEGTSToMP4(context.Background(), script, path)
	require.NoError(t, err)
	assert.False(t, IsMPEGTSFile(path), "after remux should not be MPEG-TS")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ftyp")
}

func TestRemuxMPEGTSToMP4_NoopWrongExt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.ts")
	require.NoError(t, os.WriteFile(path, []byte{0x47, 0x00}, 0o644))
	require.NoError(t, RemuxMPEGTSToMP4(context.Background(), "", path))
	// Unchanged TS.
	assert.True(t, IsMPEGTSFile(path))
}

func TestRemuxMPEGTSToMP4_MissingFFmpeg(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.mp4")
	require.NoError(t, os.WriteFile(path, []byte{0x47, 0x00}, 0o644))

	// Isolate from system ffmpeg by prepending an empty bin dir and using a
	// preferred path that does not exist.
	t.Setenv("PATH", dir)
	err := RemuxMPEGTSToMP4(context.Background(), filepath.Join(dir, "no-such-ffmpeg"), path)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "ffmpeg")
}
