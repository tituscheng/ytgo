package hlsfrag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadToFile_ConcatenatesInOrder(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-MAP:URI="init.mp4"
#EXTINF:1.0,
a.m4s
#EXTINF:1.0,
b.m4s
#EXTINF:1.0,
c.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("INIT"))
	})
	mux.HandleFunc("/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond) // out-of-order completion
		_, _ = w.Write([]byte("AAA"))
	})
	mux.HandleFunc("/b.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("BBB"))
	})
	mux.HandleFunc("/c.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("CCC"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	var lastDown, lastTot int64
	d := &Downloader{
		Client:  srv.Client(),
		Workers: 4,
		Progress: func(down, tot int64) {
			lastDown, lastTot = down, tot
		},
	}
	err := d.DownloadToFile(context.Background(), srv.URL+"/pl.m3u8", dest)
	require.NoError(t, err)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "INITAAABBBCCC", string(data))
	assert.Equal(t, int32(4), hits.Load())
	assert.Equal(t, int64(len(data)), lastDown)
	assert.Equal(t, int64(len(data)), lastTot)
}

func TestDownloadToFile_FollowsMasterToMedia(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1000
media.m3u8
`))
	})
	mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:1.0,
seg.m4s
#EXT-X-ENDLIST
`))
	})
	mux.HandleFunc("/seg.m4s", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("SEGDATA"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "o.mp4")
	d := &Downloader{Client: srv.Client(), Workers: 2}
	require.NoError(t, d.DownloadToFile(context.Background(), srv.URL+"/master.m3u8", dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "SEGDATA", string(data))
}

func TestDownloadToFile_NestedMasterErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1000
v.m3u8
`))
	}))
	defer srv.Close()

	d := &Downloader{Client: srv.Client(), Workers: 2}
	err := d.DownloadToFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "o.mp4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested master")
}

func TestDownloadToFile_RetryTransient(t *testing.T) {
	var tries atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXTINF:1.0,
seg.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/seg.m4s", func(w http.ResponseWriter, r *http.Request) {
		n := tries.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("OKDATA"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	d := &Downloader{Client: srv.Client(), Workers: 2, MaxRetries: 5}
	require.NoError(t, d.DownloadToFile(context.Background(), srv.URL+"/pl.m3u8", dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "OKDATA", string(data))
	assert.GreaterOrEqual(t, tries.Load(), int32(3))
}

func TestDownloadToFile_ResumeMidway(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXT-X-MAP:URI="init.mp4"
#EXTINF:1.0,
a.m4s
#EXTINF:1.0,
b.m4s
#EXTINF:1.0,
c.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("INIT"))
	})
	mux.HandleFunc("/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("AAA"))
	})
	mux.HandleFunc("/b.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("BBB"))
	})
	mux.HandleFunc("/c.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("CCC"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	plURL := srv.URL + "/pl.m3u8"

	// Simulate a crash after init+a were written.
	partial := []byte("INITAAA")
	require.NoError(t, os.WriteFile(dest, partial, 0o644))
	frags := []Fragment{
		{Index: 0, URL: srv.URL + "/init.mp4", IsInit: true},
		{Index: 1, URL: srv.URL + "/a.m4s"},
		{Index: 2, URL: srv.URL + "/b.m4s"},
		{Index: 3, URL: srv.URL + "/c.m4s"},
	}
	st := &ResumeState{
		Version:       resumeVersion,
		PlaylistURL:   plURL,
		FragmentCount: 4,
		NextIndex:     2,
		BytesWritten:  int64(len(partial)),
		Fingerprint:   fragmentFingerprint(frags),
	}
	require.NoError(t, saveResumeState(dest, st))

	d := &Downloader{Client: srv.Client(), Workers: 4, Continue: true}
	require.NoError(t, d.DownloadToFile(context.Background(), plURL, dest))

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "INITAAABBBCCC", string(data))
	// Only b and c should have been fetched (not init/a again).
	assert.Equal(t, int32(2), hits.Load())
	_, err = os.Stat(resumePath(dest))
	assert.True(t, os.IsNotExist(err), "sidecar should be removed on success")
}

func TestDownloadToFile_NoContinueDiscardsPartial(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `#EXTM3U
#EXTINF:1.0,
a.m4s
#EXTINF:1.0,
b.m4s
#EXT-X-ENDLIST
`)
	})
	mux.HandleFunc("/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("AAA"))
	})
	mux.HandleFunc("/b.m4s", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("BBB"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	plURL := srv.URL + "/pl.m3u8"
	require.NoError(t, os.WriteFile(dest, []byte("AAA"), 0o644))
	frags := []Fragment{
		{Index: 0, URL: srv.URL + "/a.m4s"},
		{Index: 1, URL: srv.URL + "/b.m4s"},
	}
	require.NoError(t, saveResumeState(dest, &ResumeState{
		Version:       resumeVersion,
		PlaylistURL:   plURL,
		FragmentCount: 2,
		NextIndex:     1,
		BytesWritten:  3,
		Fingerprint:   fragmentFingerprint(frags),
	}))

	d := &Downloader{Client: srv.Client(), Workers: 2, Continue: false}
	require.NoError(t, d.DownloadToFile(context.Background(), plURL, dest))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "AAABBB", string(data))
	assert.Equal(t, int32(2), hits.Load(), "both fragments re-fetched without continue")
}

func TestDownloadToFile_WindowBoundsMemory(t *testing.T) {
	// Many small fragments with limited workers; ensure ordered concat still works
	// (sliding window must not reorder or drop data).
	const n = 40
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "#EXTINF:1.0,\n%d.m4s\n", i)
		}
		fmt.Fprintf(w, "#EXT-X-ENDLIST\n")
	})
	for i := 0; i < n; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/%d.m4s", i), func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			// Stagger completions so workers finish out of order.
			time.Sleep(time.Duration(i%5) * time.Millisecond)
			_, _ = fmt.Fprintf(w, "S%02d", i)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.mp4")
	d := &Downloader{Client: srv.Client(), Workers: 4, Continue: true}
	require.NoError(t, d.DownloadToFile(context.Background(), srv.URL+"/pl.m3u8", dest))

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	var want string
	for i := 0; i < n; i++ {
		want += fmt.Sprintf("S%02d", i)
	}
	assert.Equal(t, want, string(data))
	assert.Equal(t, int32(n), hits.Load())
}

func TestValidateResume_Mismatch(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "x.part")
	require.NoError(t, os.WriteFile(dest, []byte("ab"), 0o644))
	frags := []Fragment{{URL: "http://a"}, {URL: "http://b"}}
	st := &ResumeState{
		Version:       resumeVersion,
		PlaylistURL:   "http://pl",
		FragmentCount: 2,
		NextIndex:     1,
		BytesWritten:  2,
		Fingerprint:   fragmentFingerprint(frags),
	}
	assert.True(t, validateResume(st, "http://pl", frags, dest))
	assert.False(t, validateResume(st, "http://other", frags, dest))
	// File longer than checkpoint is still valid (will truncate on resume).
	require.NoError(t, os.WriteFile(dest, []byte("abXXX"), 0o644))
	assert.True(t, validateResume(st, "http://pl", frags, dest))
	// File shorter than checkpoint is corrupt.
	require.NoError(t, os.WriteFile(dest, []byte("a"), 0o644))
	assert.False(t, validateResume(st, "http://pl", frags, dest))
}
