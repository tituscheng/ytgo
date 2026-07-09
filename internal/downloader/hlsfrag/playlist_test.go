package hlsfrag

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMediaPlaylist_fMP4(t *testing.T) {
	const doc = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:3
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-MAP:URI="init.mp4"
#EXTINF:3.000000,
0.m4s
#EXTINF:3.000000,
1.m4s
#EXT-X-ENDLIST
`
	pl, err := ParseMediaPlaylist(strings.NewReader(doc), "https://cdn.example/video/manifest.m3u8")
	require.NoError(t, err)
	require.Len(t, pl.Fragments, 3)
	assert.True(t, pl.HasEndList)
	assert.False(t, pl.Encrypted)
	assert.False(t, pl.IsMaster)
	assert.True(t, pl.Fragments[0].IsInit)
	assert.Equal(t, "https://cdn.example/video/init.mp4", pl.Fragments[0].URL)
	assert.Equal(t, "https://cdn.example/video/0.m4s", pl.Fragments[1].URL)
	assert.Equal(t, "https://cdn.example/video/1.m4s", pl.Fragments[2].URL)
	assert.Equal(t, 0, pl.Fragments[0].Index)
	assert.Equal(t, 2, pl.Fragments[2].Index)
}

func TestParseMediaPlaylist_MasterRejected(t *testing.T) {
	const doc = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
stream_360.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1400000,RESOLUTION=1280x720
stream_720.m3u8
`
	pl, err := ParseMediaPlaylist(strings.NewReader(doc), "https://cdn.example/master.m3u8")
	require.Error(t, err)
	assert.True(t, pl == nil || pl.IsMaster)
}

func TestParseMediaPlaylist_Encrypted(t *testing.T) {
	const doc = `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="key.bin"
#EXTINF:3.0,
0.ts
#EXT-X-ENDLIST
`
	pl, err := ParseMediaPlaylist(strings.NewReader(doc), "https://cdn.example/p.m3u8")
	require.NoError(t, err)
	assert.True(t, pl.Encrypted)
}

func TestParseMediaPlaylist_AbsoluteURIs(t *testing.T) {
	const doc = `#EXTM3U
#EXT-X-MAP:URI="https://other.example/init.mp4"
#EXTINF:1.0,
https://other.example/a.m4s#cell=cf
#EXT-X-ENDLIST
`
	pl, err := ParseMediaPlaylist(strings.NewReader(doc), "https://cdn.example/p.m3u8")
	require.NoError(t, err)
	require.Len(t, pl.Fragments, 2)
	assert.Equal(t, "https://other.example/init.mp4", pl.Fragments[0].URL)
	assert.Equal(t, "https://other.example/a.m4s", pl.Fragments[1].URL)
}

func TestResolveWorkers(t *testing.T) {
	assert.Equal(t, DefaultWorkers, ResolveWorkers(0))
	assert.Equal(t, DefaultWorkers, ResolveWorkers(1))
	assert.Equal(t, 4, ResolveWorkers(4))
	assert.Equal(t, MaxWorkers, ResolveWorkers(100))
}
