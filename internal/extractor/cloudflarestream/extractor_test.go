package cloudflarestream

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServerHost(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

func TestSuitable(t *testing.T) {
	e := NewExtractor(0)

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "embed js",
			url:  "https://embed.cloudflarestream.com/embed/we4g.fla9.latest.js?video=31c9291ab41fac05471db4e73aa11717",
			want: true,
		},
		{
			name: "watch page",
			url:  "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1",
			want: true,
		},
		{
			name: "manifest",
			url:  "https://cloudflarestream.com/31c9291ab41fac05471db4e73aa11717/manifest/video.mpd",
			want: true,
		},
		{
			name: "videodelivery embed",
			url:  "https://embed.videodelivery.net/embed/r4xu.fla9.latest.js?video=81d80727f3022488598f68d323c1ad5e",
			want: true,
		},
		{
			name: "customer iframe",
			url:  "https://customer-aw5py76sw8wyqzmh.cloudflarestream.com/2463f6d3e06fa29710a337f5f5389fd8/iframe",
			want: true,
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

func TestParseVideoURL(t *testing.T) {
	t.Run("direct hex id", func(t *testing.T) {
		parsed, err := parseVideoURL("https://cloudflarestream.com/31c9291ab41fac05471db4e73aa11717")
		require.NoError(t, err)
		assert.Equal(t, "31c9291ab41fac05471db4e73aa11717", parsed.rawID)
		assert.Equal(t, "31c9291ab41fac05471db4e73aa11717", parsed.displayID)
		assert.Equal(t, "cloudflarestream.com", parsed.manifestHost)
		assert.Equal(
			t,
			"https://cloudflarestream.com/31c9291ab41fac05471db4e73aa11717/manifest/video.m3u8",
			parsed.masterManifestURL(),
		)
	})

	t.Run("customer subdomain preserved", func(t *testing.T) {
		parsed, err := parseVideoURL(
			"https://customer-aw5py76sw8wyqzmh.cloudflarestream.com/2463f6d3e06fa29710a337f5f5389fd8/iframe",
		)
		require.NoError(t, err)
		assert.Equal(t, "customer-aw5py76sw8wyqzmh.cloudflarestream.com", parsed.manifestHost)
	})

	t.Run("embed js query param", func(t *testing.T) {
		parsed, err := parseVideoURL(
			"https://embed.cloudflarestream.com/embed/we4g.fla9.latest.js?video=31c9291ab41fac05471db4e73aa11717",
		)
		require.NoError(t, err)
		assert.Equal(t, "31c9291ab41fac05471db4e73aa11717", parsed.displayID)
	})

	t.Run("url encoded jwt path", func(t *testing.T) {
		token := "eyJhbGci.test.abc_def-ghi"
		encoded := strings.ReplaceAll(token, "_", "%5F")
		parsed, err := parseVideoURL("https://watch.cloudflarestream.com/" + encoded)
		require.NoError(t, err)
		assert.Equal(t, token, parsed.rawID)
	})
}

func TestParseMasterPlaylist(t *testing.T) {
	const sample = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",URI="subs/en.vtt"
#EXT-X-STREAM-INF:RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2",BANDWIDTH=3600000,SCORE=4.0,FRAME-RATE=29.970
stream_720.m3u8
#EXT-X-STREAM-INF:RESOLUTION=1920x1080,CODECS="avc1.4d4028,mp4a.40.2",BANDWIDTH=5200000,SCORE=5.0,FRAME-RATE=29.970
stream_1080.m3u8
`

	result, err := parseMasterPlaylist(strings.NewReader(sample), "https://cloudflarestream.com/demo/manifest/video.m3u8")
	require.NoError(t, err)
	require.Len(t, result.formats, 2)
	require.Len(t, result.subtitles["en"], 1)

	assert.Equal(t, "hls-720p", result.formats[0].FormatID)
	assert.Equal(t, "avc1.4d401f", result.formats[0].VideoCodec)
	assert.Equal(t, "mp4a.40.2", result.formats[0].AudioCodec)
	assert.Equal(
		t,
		"https://cloudflarestream.com/demo/manifest/subs/en.vtt",
		result.subtitles["en"][0].URL,
	)
}

func TestFetchDASH(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifest/video.mpd") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	parsed := parsedVideo{
		rawID:        "deadbeefdeadbeefdeadbeefdeadbeef",
		displayID:    "deadbeefdeadbeefdeadbeefdeadbeef",
		manifestHost: testServerHost(t, srv),
	}
	e := NewExtractor(0)
	e.client = srv.Client()

	formats, err := e.fetchDASH(t.Context(), parsed)
	require.NoError(t, err)
	require.Len(t, formats, 1)
	assert.Equal(t, "dash", formats[0].FormatID)
	assert.True(t, strings.HasSuffix(formats[0].URL, "/manifest/video.mpd"))
}

func TestProbeDirectMP4(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/downloads/default.mp4") {
			w.Header().Set("Content-Length", "1234567")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	parsed := parsedVideo{
		rawID:        "deadbeefdeadbeefdeadbeefdeadbeef",
		displayID:    "deadbeefdeadbeefdeadbeefdeadbeef",
		manifestHost: testServerHost(t, srv),
	}
	e := NewExtractor(0)
	e.client = srv.Client()

	format, ok := e.probeDirectMP4(t.Context(), parsed, 720)
	require.True(t, ok)
	assert.Equal(t, "mp4-direct", format.FormatID)
	assert.Equal(t, int64(1234567), format.Filesize)
	assert.Equal(t, 721, format.Height)
}

func TestFetchHLS(t *testing.T) {
	const sample = `#EXTM3U
#EXT-X-STREAM-INF:RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",BANDWIDTH=800000
stream_360.m3u8
`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/manifest/video.m3u8") {
			_, _ = w.Write([]byte(sample))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	parsed := parsedVideo{
		rawID:        "deadbeefdeadbeefdeadbeefdeadbeef",
		displayID:    "deadbeefdeadbeefdeadbeefdeadbeef",
		manifestHost: testServerHost(t, srv),
	}
	e := NewExtractor(0)
	e.client = srv.Client()

	result, err := e.fetchHLS(t.Context(), parsed)
	require.NoError(t, err)
	require.Len(t, result.formats, 1)
	assert.Equal(t, "hls-360p", result.formats[0].FormatID)
}
