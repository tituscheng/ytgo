package rumble

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
)

func TestSuitable(t *testing.T) {
	e := NewExtractor(0)

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "embed",
			url:  "https://rumble.com/embed/v5pv5f",
			want: true,
		},
		{
			name: "embed with prefix",
			url:  "https://rumble.com/embed/ufe9n.v5pv5f",
			want: true,
		},
		{
			name: "video page",
			url:  "https://rumble.com/vdmum1-moose-the-dog-helps-girls-dig-a-snow-fort.html",
			want: true,
		},
		{
			name: "channel page",
			url:  "https://rumble.com/c/Styxhexenhammer666",
			want: false,
		},
		{
			name: "browse",
			url:  "https://rumble.com/browse/live",
			want: false,
		},
		{
			name: "youtube",
			url:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, e.Suitable(tt.url))
		})
	}
}

func TestParseEmbedVideoID(t *testing.T) {
	id, ok := parseEmbedVideoID("https://rumble.com/embed/ufe9n.v5pv5f")
	require.True(t, ok)
	assert.Equal(t, "v5pv5f", id)
}

func TestParseFormats(t *testing.T) {
	video := &embedResponse{
		FPS: 29.97,
		UA: map[string]json.RawMessage{
			"mp4": json.RawMessage(`{"360":{"url":"https://cdn.example/360.mp4","meta":{"bitrate":631,"size":100,"w":640,"h":360}},"720":{"url":"https://cdn.example/720.mp4","meta":{"bitrate":1957,"size":200,"w":1280,"h":720}}}`),
			"hls": json.RawMessage(`{"auto":{"url":"https://rumble.com/live-hls/test/playlist.m3u8","meta":{"live":true}}}`),
		},
	}

	formats := parseFormats(video)
	require.Len(t, formats, 3)

	byID := indexFormats(formats)
	assert.Equal(t, 360, byID["mp4-360p"].Height)
	assert.Equal(t, "https://cdn.example/720.mp4", byID["mp4-720p"].URL)
	assert.Equal(t, "https://rumble.com/live-hls/test/playlist.m3u8", byID["hls-auto"].URL)
}

func indexFormats(formats []extractor.Format) map[string]extractor.Format {
	out := make(map[string]extractor.Format, len(formats))
	for _, f := range formats {
		out[f.FormatID] = f
	}
	return out
}

func TestExtractFromMockAPI(t *testing.T) {
	const embedJSON = `{
		"title":"Test Video",
		"duration":120,
		"pubDate":"2019-10-20T22:52:48+00:00",
		"fps":29.97,
		"author":{"name":"Channel","url":"https://rumble.com/c/Test"},
		"i":"https://cdn.example/thumb.jpg",
		"ua":{"mp4":{"480":{"url":"https://cdn.example/video.mp4","meta":{"bitrate":810,"size":1234,"w":854,"h":480}}}}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/embedJS/u3/") {
			_, _ = w.Write([]byte(embedJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	video, err := fetchEmbedJSON(t.Context(), srv.Client(), srv.URL+"/embedJS/u3/", "v5pv5f")
	require.NoError(t, err)
	assert.Equal(t, "Test Video", video.Title)

	formats := parseFormats(video)
	require.Len(t, formats, 1)
	assert.Equal(t, "mp4-480p", formats[0].FormatID)
}

func TestVideoIDFromPageHTML(t *testing.T) {
	const pageHTML = `<html><body><script>Rumble("play", {"video":"vb0ofn"})</script></body></html>`
	id, ok := videoIDFromPageHTML([]byte(pageHTML))
	require.True(t, ok)
	assert.Equal(t, "vb0ofn", id)
}
