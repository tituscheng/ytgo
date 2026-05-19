# ytgo Future Roadmap

This document captures the next major features and improvements for ytgo. Each item includes the problem, proposed approach, and key technical considerations.

---

## ✅ Recently Implemented

| Feature | Status | PR |
|---|---|---|
| Format preference scoring (`PreferVideoCodec`, `PreferAudioCodec`, `PreferContainer`) | ✅ Done | — |
| Go-native `FormatFilter` func | ✅ Done | — |
| Auto-faststart for MP4/M4A outputs | ✅ Done | — |
| Structured `OnProgress` callback with multi-format aggregation | ✅ Done | — |
| `api.GetStreamURL()` with rich metadata & preferences | ✅ Done | — |
| Opt-in metadata enrichment (`--enrich-metadata`) for likes | ✅ Done | — |

---

---

## 1. Fix `--cookies-from-browser` for Age-Restricted Content

### Problem
YouTube age-restricted and members-only videos require authentication cookies. The flag `--cookies-from-browser chrome` exists in the CLI but is not wired to the extractor.

### Current State
- `internal/config/config.go` defines `CookiesFromBrowser string`
- `cmd/root.go` binds the flag
- The custom Innertube client (`internal/extractor/youtube/innertube/`) accepts a custom `*http.Client`. Cookies can be injected via the cookie jar.

### Proposed Approach

1. **Browser Cookie Extraction**
   - Use `github.com/zellyn/kooky` (cross-platform browser cookie extraction) or platform-specific libraries:
     - macOS: `~/Library/Application Support/Google/Chrome/Default/Cookies` (SQLite, encrypted with Keychain)
     - Linux: `~/.config/google-chrome/Default/Cookies`
     - Windows: `%LOCALAPPDATA%\Google\Chrome\User Data\Default\Cookies`
   - Support Chrome, Firefox, Safari, Edge

2. **Cookie Injection**
   - Our `innertube.Client` accepts a custom `*http.Client`. We can inject a cookie jar:
     ```go
     jar, _ := cookiejar.New(nil)
     // populate jar with extracted cookies for .youtube.com
     client.HTTPClient.Jar = jar
     ```

3. **Integration Points**
   - `internal/extractor/youtube/innertube/client.go`: Add `SetCookies(cookies []*http.Cookie)` method
   - `internal/core/engine.go`: After `Register()`, if `cfg.CookiesFromBrowser != ""`, extract and inject
   - Consider `yt-dlp`'s cookie format (`# Netscape HTTP Cookie File`) as a fallback via `--cookies cookies.txt`

### Open Questions
- Should we support cookie refresh (re-extract if expired) or one-shot extraction?
- How to handle Chrome's SQLite encryption on macOS (Keychain access requires user prompt)?

---

## 2. Add Concurrent Fragment Downloader for HLS/DASH Streams

### Problem
DASH (MPD) and HLS (M3U8) manifests split video into small fragments (~2–10s). Our current `downloader.Downloader` fetches a single URL. Adaptive formats with manifest URLs (`DASHManifestURL`, `HLSManifestURL`) are not downloadable.

### Current State
- `downloader/downloader.go` supports single-file HTTP download with resume
- `Format` has `ManifestURL` field but it is unused
- `ConcurrentFragments` flag exists but is ignored

### Proposed Approach

1. **Manifest Parser**
   - New package `internal/downloader/manifest/`
   - `ParseDASH(mpdURL string) ([]Fragment, error)` — parse XML MPD, extract segment URLs
   - `ParseHLS(m3u8URL string) ([]Fragment, error)` — parse M3U8 playlist, resolve relative URLs

2. **Fragment Structure**
   ```go
   type Fragment struct {
       URL      string
       StartByte int64
       EndByte   int64
       Sequence int // for ordering
   }
   ```

3. **Concurrent Download Worker Pool**
   ```go
   type FragmentDownloader struct {
       Client      *http.Client
       Workers     int
       Fragments   []Fragment
       OutFile     string
   }
   ```
   - Spawn `N` goroutines (`cfg.ConcurrentFragments`, default 1)
   - Each worker pulls from a `chan Fragment`
   - Downloaded fragments are written to a temp file in sequence order
   - A separate goroutine stitches fragments in-order into the final file
   - Progress callback reports overall bytes downloaded

4. **Integration**
   - In `core/engine.go`, if `f.ManifestURL != ""`, use `FragmentDownloader` instead of single-file download
   - Fallback: some DASH formats have both direct `URL` and `ManifestURL` — prefer direct URL when available

### Key Design Decisions
- **Memory vs. Disk**: For large videos (4K), buffer fragments to disk, not memory
- **Resume for fragments**: Track completed sequences in a `.fragments` metadata file
- **Bandwidth shaping**: Respect `--limit-rate` if added in the future

---

## 3. Wire Up Full Embed Pipeline (Subs + Thumbnail + Chapters)

### Problem
The FFmpeg postprocessors exist but are not fully integrated. Specifically:
- `--embed-subs` flag exists but subtitle files are not passed to the embedder
- `--embed-thumbnail` works for audio but not fully for video
- `--embed-chapters` is accepted but chapters are not written to a metadata file for FFmpeg

### Current State
- `postprocessor/postprocessor.go` defines `Embedder.Run(ctx, path, info, EmbedOptions)`
- `Embedder.Run` creates an `FFMETADATA1` file for chapters and passes thumbnails to FFmpeg
- `core/engine.go` calls `embedder.Run()` but subtitle paths are not collected

### Proposed Approach

1. **Collect Subtitle Paths**
   - In `core/engine.go`, after `writeSubtitles()`, capture the returned paths
   - Pass subtitle paths to `Embedder` via a new `EmbedOptions.SubtitleFiles []string`

2. **Embedder Improvements**
   ```go
   type EmbedOptions struct {
       Metadata      bool
       Thumbnail     bool
       Subtitles     bool
       Chapters      bool
       SubtitleFiles []string
       SubFormat     string // srt, vtt
   }
   ```
   - For **subtitles in MP4/MKV**: Use `ffmpeg -i video.mp4 -i subs.srt -c copy -c:s mov_text output.mp4`
   - For **subtitles in MKV**: Use `ffmpeg ... -c:s srt`
   - For **thumbnails in video**: Map thumbnail as video stream 1 with `attached_pic` disposition
   - For **chapters**: Write `FFMETADATA1` file and pass `-i chapters.txt -map_metadata 1`

3. **Container-Aware Logic**
   | Container | Subtitle Codec | Thumbnail Support | Notes |
   |---|---|---|---|
   | MP4 | `mov_text` | Yes (via `attached_pic`) | Most compatible |
   | MKV | `srt`/`ass`/`vtt` | Yes | Best for subtitles |
   | WEBM | Limited | No | Skip embed for webm |

4. **Integration Flow**
   ```
   Download → Merge (if needed) → Convert (if -x) → Embed (metadata + thumbnail + subs + chapters)
   ```

### Testing Strategy
- Mock FFmpeg with shell scripts (as done in `postprocessor_test.go`)
- Verify correct `-map`, `-disposition`, and `-c:s` arguments are generated

---

## 4. Add More Extractors (The Interface Is Ready)

### Problem
Currently only YouTube is supported. The `InfoExtractor` interface is designed for multi-site support.

### Current State
```go
type InfoExtractor interface {
    Name() string
    Suitable(url string) bool
    Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error)
}
```

### Proposed Approach

1. **Extractor Registry**
   - New file `internal/extractor/registry.go`:
     ```go
     var DefaultExtractors = []InfoExtractor{
         youtube.NewExtractor(30 * time.Second),
         vimeo.NewExtractor(30 * time.Second),
         // ...
     }
     ```

2. **Priority-Ordered Matching**
   - `Suitable()` is checked in order; first match wins
   - YouTube should remain first (most common)

3. **Candidate Sites**
   | Site | Difficulty | Notes |
   |---|---|---|
   | **Vimeo** | Low | Has a simple oEmbed + player config API |
   | **Reddit** | Medium | Requires parsing JSON embedded in HTML |
   | **Twitter/X** | Medium | Requires bearer token + guest token dance |
   | **TikTok** | High | Heavily obfuscated, frequent API changes |
   | **SoundCloud** | Low | Has public API with client_id |
   | **Bilibili** | Medium | Innertube-like API |

4. **Vimeo Extractor (MVP Example)**
   ```go
   type VimeoExtractor struct{ client *http.Client }

   func (e *VimeoExtractor) Suitable(url string) bool {
       return strings.Contains(url, "vimeo.com")
   }

   func (e *VimeoExtractor) Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error) {
       // 1. Fetch oEmbed: https://vimeo.com/api/oembed.json?url=...
       // 2. Fetch player config from page or API
       // 3. Extract progressive / DASH streams
       // 4. Return VideoInfo
   }
   ```

### Design Principle
Each extractor lives in its own package (`internal/extractor/<site>/`). Extractors should be **stateless** except for the HTTP client. All site-specific parsing, authentication, and error handling is isolated.

---

## 5. Performance Optimization

### Problem
Current bottlenecks:
- Single-threaded playlist processing
- Default `http.Client` without connection pooling
- No download speed limiting
- Fragments are not downloaded concurrently

### Current State
- `downloader.Downloader` uses a fresh `http.Client` per instance
- `core/engine.go` processes playlist entries sequentially
- `ConcurrentFragments` flag is ignored

### Proposed Approach

#### A. Connection Pooling & HTTP Client Tuning
```go
func NewOptimizedClient(timeout time.Duration) *http.Client {
    transport := &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        DisableCompression:  false,
    }
    return &http.Client{
        Timeout:   timeout,
        Transport: transport,
    }
}
```
- Share one `http.Client` across the engine, extractors, and downloaders
- Enable HTTP/2 (Go does this automatically with `http.Transport`)

#### B. Parallel Playlist Workers
```go
func (e *Engine) runPlaylist(ctx context.Context, info *extractor.VideoInfo) error {
    workerCount := e.Config.ConcurrentFragments
    if workerCount < 1 {
        workerCount = 1
    }

    jobs := make(chan *extractor.VideoInfo, len(info.Entries))
    var wg sync.WaitGroup

    for i := 0; i < workerCount; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for entry := range jobs {
                _ = e.runVideo(ctx, entry)
            }
        }()
    }

    for _, entry := range info.Entries {
        jobs <- entry
    }
    close(jobs)
    wg.Wait()
    return nil
}
```
- Respects `--playlist-start` and `--playlist-end`
- Order is not preserved (acceptable for downloads)
- Add `--max-downloads` to limit total concurrent jobs

#### C. Download Speed Limiting
- Add `--limit-rate` flag (bytes/sec)
- Implement a `io.Reader` wrapper that sleeps to maintain rate:
  ```go
  type ThrottledReader struct {
      r       io.Reader
      limiter *rate.Limiter // golang.org/x/time/rate
  }
  ```

#### D. Memory Optimization for Large Files
- Use `io.CopyBuffer` with a reusable byte pool (`sync.Pool`) instead of allocating a new 32KB buffer per download
- For fragment downloads, pre-allocate the output file (`fallocate` or `os.Truncate`) to avoid fragmentation

### Benchmarking Plan
- Add benchmarks in `internal/downloader/downloader_test.go`:
  - `BenchmarkDownloadSingle`
  - `BenchmarkDownloadResume`
  - `BenchmarkFormatSelect` (test selector performance with 100+ formats)
- Use `GODEBUG=gctrace=1` to monitor GC pressure during large playlist downloads

---

## Cross-Cutting Concerns

### Error Handling & Resilience
- Add `--retries` and `--retry-sleep` flags
- Implement exponential backoff for 429/503 responses
- Distinguish between retryable errors (network) and fatal errors (video removed)

### Observability
- Migrate from `fmt.Fprintf(os.Stderr, ...)` to `log/slog` with structured logging
- Add debug-level HTTP request/response dumps (behind `--verbose`)
- Export Prometheus metrics if running as a library in a server context

### Testing
- Add integration tests that use `httptest` to mock YouTube's innertube API
- Add golden file tests for format selector edge cases
- Test FFmpeg postprocessors with actual FFmpeg in CI (or skip if unavailable)

---

*This roadmap is iterative. Pick any item and implement it in isolation — the architecture is designed for minimal cross-package coupling.*
