package template

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tituscheng/ytgo/pkg/ytgo"
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
	// Leading restricted chars become "-", then must be trimmed so the
	// filename does not start with "-" (ffmpeg option-parser trap).
	assert.Equal(t,
		"I Wouldn't Deny Him-- Following Christ Through Persecution - Brother Yun & Pastor Michael Koulianos",
		sanitize(`"I Wouldn't Deny Him"| Following Christ Through Persecution | Brother Yun & Pastor Michael Koulianos`),
	)
	assert.Equal(t, "Dashy", sanitize("-Dashy-"))
	assert.Equal(t, "Leading", sanitize(`"Leading`))
	assert.Equal(t, "Trailing", sanitize(`Trailing|`))
}

func TestParseLeadingDashTitle(t *testing.T) {
	// Real YouTube title that previously produced "-I Wouldn't….mp4" and
	// caused "ffmpeg merge: Unrecognized option 'I Wouldn't…'".
	info := &ytgo.VideoInfo{
		ID:    "fZHouoxG644",
		Title: `"I Wouldn't Deny Him"| Following Christ Through Persecution | Brother Yun & Pastor Michael Koulianos`,
	}
	got := Parse("%(title)s [%(id)s].%(ext)s", info, "mp4")
	assert.Equal(t, "I Wouldn't Deny Him-- Following Christ Through Persecution - Brother Yun & Pastor Michael Koulianos [fZHouoxG644].mp4", got)
	assert.False(t, strings.HasPrefix(got, "-"), "output path must not start with '-'")
}

func TestBuildPath(t *testing.T) {
	info := &ytgo.VideoInfo{ID: "x", Title: "t"}
	assert.Equal(t, "/tmp/t [x].mp4", BuildPath("%(title)s [%(id)s].%(ext)s", info, "mp4", "/tmp"))
}
