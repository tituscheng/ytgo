package dailymotion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			name: "short url",
			url:  "https://dai.ly/x5kesuj",
			want: true,
		},
		{
			name: "video page",
			url:  "https://www.dailymotion.com/video/x5kesuj_office-christmas-party_news",
			want: true,
		},
		{
			name: "embed",
			url:  "https://www.dailymotion.com/embed/video/x8u4owg",
			want: true,
		},
		{
			name: "crawler",
			url:  "https://www.dailymotion.com/crawler/video/x8u4owg",
			want: true,
		},
		{
			name: "geo player",
			url:  "https://geo.dailymotion.com/player/x86gw.html?video=x89eyek",
			want: true,
		},
		{
			name: "long video id",
			url:  "https://geo.dailymotion.com/player/x86gw.html?video=k46oCapRs4iikoz9DWy",
			want: true,
		},
		{
			name: "playlist page",
			url:  "https://www.dailymotion.com/playlist/xv4bw",
			want: false,
		},
		{
			name: "geo player playlist",
			url:  "https://geo.dailymotion.com/player/xf7zn.html?playlist=x7wdsj",
			want: false,
		},
		{
			name: "user page",
			url:  "https://www.dailymotion.com/user/nqtv",
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

func TestParseVideoID(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "short url",
			url:  "https://dai.ly/x5kesuj",
			want: "x5kesuj",
		},
		{
			name: "video page",
			url:  "https://www.dailymotion.com/video/x5kesuj_slug",
			want: "x5kesuj",
		},
		{
			name: "geo player",
			url:  "https://geo.dailymotion.com/player.html?video=k46oCapRs4iikoz9DWy",
			want: "k46oCapRs4iikoz9DWy",
		},
		{
			name:    "playlist",
			url:     "https://www.dailymotion.com/playlist/xv4bw",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVideoID(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseFormats(t *testing.T) {
	metadata := &metadataResponse{
		Qualities: map[string][]qualityEntry{
			"380": {
				{
					URL: "https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/380.mp4?sec=abc",
				},
			},
			"720": {
				{
					URL:  "https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/H264-1280x720-60.mp4?sec=abc",
					Type: "",
				},
			},
			"1080": {
				{
					URL:  "https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/1080.m3u8#cell=cf",
					Type: "application/x-mpegURL",
				},
			},
			"240": {
				{
					URL:  "https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/240.lj",
					Type: lumberjackManifest,
				},
			},
		},
	}

	formats := parseFormats(metadata)
	require.Len(t, formats, 3)

	byID := indexFormats(formats)
	assert.Equal(t, "http-380", byID["http-380"].FormatID)
	assert.Equal(t, 720, byID["http-720"].Height)
	assert.Equal(t, 60.0, byID["http-720"].FPS)
	assert.Equal(t, "hls-1080", byID["hls-1080"].FormatID)
	assert.Equal(t, "https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/1080.m3u8", byID["hls-1080"].ManifestURL)
}

func TestParseSubtitles(t *testing.T) {
	metadata := &metadataResponse{}
	metadata.Subtitles.Data = map[string]subtitleEntry{
		"en": {URLs: []string{"https://cdn.example/sub-en.vtt"}},
		"fr": {URLs: []string{"https://cdn.example/sub-fr.vtt"}},
	}

	subs := parseSubtitles(metadata)
	require.Len(t, subs, 2)
	assert.Equal(t, "https://cdn.example/sub-en.vtt", subs["en"][0].URL)
	assert.Equal(t, "vtt", subs["en"][0].Ext)
}

func TestParseThumbnails(t *testing.T) {
	metadata := &metadataResponse{
		Posters: map[string]string{
			"720": "https://s1.dmcdn.net/v/poster720.jpg",
		},
		Thumbnails: map[string]string{
			"1080": "https://s2.dmcdn.net/v/thumb1080.jpg",
		},
	}

	thumbs := parseThumbnails(metadata)
	require.Len(t, thumbs, 2)
}

func TestExtractFromMockAPI(t *testing.T) {
	const metadataJSON = `{
		"title":"Test Video",
		"description":"A test description",
		"duration":187,
		"created_time":1493651285,
		"owner":{"id":"x1xm8ri","screenname":"Deadline"},
		"posters":{"720":"https://s1.dmcdn.net/v/poster.jpg"},
		"qualities":{
			"720":[{"url":"https://cdndirector.dailymotion.com/cdn/video/x5kesuj/video/H264-1280x720.mp4?sec=abc"}]
		},
		"subtitles":{"data":{"en":{"urls":["https://cdn.example/sub.vtt"]}}}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/player/metadata/video/x5kesuj") {
			_, _ = w.Write([]byte(metadataJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ext := &Extractor{
		client:       srv.Client(),
		metadataBase: srv.URL + "/player/metadata/video/",
	}

	info, err := ext.Extract(context.Background(), "https://dai.ly/x5kesuj")
	require.NoError(t, err)
	assert.Equal(t, "x5kesuj", info.ID)
	assert.Equal(t, "Test Video", info.Title)
	assert.Equal(t, "Deadline", info.Uploader)
	assert.Equal(t, "20170501", info.UploadDate)
	assert.Equal(t, 187*time.Second, info.Duration)
	assert.Equal(t, "https://www.dailymotion.com/video/x5kesuj", info.WebpageURL)
	require.Len(t, info.Formats, 1)
	assert.Equal(t, "http-720", info.Formats[0].FormatID)
	require.Contains(t, info.Subtitles, "en")
}

func TestMetadataError(t *testing.T) {
	const metadataJSON = `{
		"error":{"code":"DM007","title":"This video is not available in your country"}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(metadataJSON))
	}))
	defer srv.Close()

	ext := &Extractor{
		client:       srv.Client(),
		metadataBase: srv.URL + "/player/metadata/video/",
	}

	_, err := ext.Extract(context.Background(), "https://dai.ly/x5kesuj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "geo-restricted")
}

func indexFormats(formats []extractor.Format) map[string]extractor.Format {
	out := make(map[string]extractor.Format, len(formats))
	for _, f := range formats {
		out[f.FormatID] = f
	}
	return out
}

func TestMetadataResponseUnmarshal(t *testing.T) {
	raw := `{
		"title":"Office Christmas Party Review",
		"duration":187,
		"created_time":1493651285,
		"owner":{"id":"x1xm8ri","screenname":"Deadline"},
		"qualities":{"720":[{"url":"https://cdn.example/720.mp4"}]}
	}`
	var metadata metadataResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &metadata))
	assert.Equal(t, "Office Christmas Party Review", metadata.Title)
}
