package template

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"ytgo/pkg/ytgo"
)

func TestParseBasic(t *testing.T) {
	info := &ytgo.VideoInfo{
		ID:    "abc123",
		Title: "My Video",
	}
	assert.Equal(t, "My Video [abc123].mp4", Parse("%(title)s [%(id)s].%(ext)s", info, "mp4"))
}

func TestParseUploadDate(t *testing.T) {
	info := &ytgo.VideoInfo{
		ID:         "abc123",
		Title:      "My Video",
		UploadDate: "20240115",
	}
	assert.Equal(t, "2024-01-15 - My Video.mp4", Parse("%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s", info, "mp4"))
}

func TestParsePlaylist(t *testing.T) {
	info := &ytgo.VideoInfo{
		ID:            "abc123",
		Title:         "My Video",
		PlaylistIndex: 5,
		PlaylistTitle: "My Playlist",
	}
	assert.Equal(t, "005 - My Video.mp4", Parse("%(playlist_index)s - %(title)s.%(ext)s", info, "mp4"))
}

func TestSanitize(t *testing.T) {
	assert.Equal(t, "My-Video", sanitize("My/Video"))
	assert.Equal(t, "My-Video", sanitize("My\\Video"))
	assert.Equal(t, "My-Video", sanitize("My:Video"))
	// Path traversal protection (Issue 2)
	assert.Equal(t, "Video", sanitize("..Video"))
	assert.Equal(t, "Video", sanitize("Video.."))
	assert.Equal(t, "Par_ent", sanitize("Par..ent")) // internal .. becomes single _
	assert.Equal(t, "Video", sanitize("...Video..."))
	assert.Equal(t, "Video", sanitize(".Video."))
}

func TestBuildPath(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "x", Title: "t"}
	assert.Equal(t, "/tmp/t [x].mp4", BuildPath("%(title)s [%(id)s].%(ext)s", info, "mp4", "/tmp"))
}
