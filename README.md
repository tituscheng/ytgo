# ytgo

A **Go** rewrite of [yt-dlp](https://github.com/yt-dlp/yt-dlp) focused on **YouTube** support.
Designed as both a standalone CLI tool and a reusable Go library.

---

## Why ytgo?

| | ytgo | yt-dlp |
|---|---|---|
| **Binary size** | ~11 MB | ~17 MB (needs CPython) |
| **Cold start** | ~0 ms | ~90 ms (Python interpreter) |
| **Extraction** | ~0.4 s | ~1.3–2.0 s |
| **Download (audio, 65 MB)** | ~4 s | ~6 s |
| **Download (video+audio, 1 GB)** | ~68 s | ~82 s |
| **Playlist (15 items)** | ~0.4 s | ~15 s |
| **Memory (list-formats)** | ~12 MB | ~82 MB |
| **JS engine** | None required | Required for sig deciphering |
| **Python runtime** | None required | Required |

ytgo uses a custom **YouTube Innertube client** with the `ANDROID_VR` client profile — it gets direct stream URLs with no JavaScript execution and no signature deciphering. Downloads use **bounded 10 MB chunk segmentation** to bypass YouTube CDN throttling, achieving full bandwidth speeds (~20+ MB/s) even on formats that would otherwise drop to ~32 KB/s. The `WEB_EMBEDDED_PLAYER` client provides fallback for age-restricted content.

If you need sponsorblock, 1000+ site extractors, or `--cookies-from-browser`, yt-dlp is still the tool for the job. ytgo is for when you want a fast, light, Go-native YouTube downloader.

---

## Features

- **YouTube video & playlist extraction** via a custom Innertube client (no JS engine)
- **Format selection** with yt-dlp-style selectors (`bv*+ba/best`, `best[height<=720]`, itag, extension)
- **Format preferences** — type-safe codec/container scoring (`PreferVideoCodec: "avc1"`) instead of regex DSL
- **HTTP download** with bounded chunk segmentation (~10 MB), concurrent workers, resume support, and progress spinners
- **Post-processing** via FFmpeg: merge, audio extraction, metadata/thumbnail/chapter embedding
- **Subtitles**: download manual & auto-generated captions, convert JSON3 → SRT/VTT
- **Output templates**: `%(title)s`, `%(upload_date>%Y-%m-%d)s`, `%(playlist_index)s`, etc.
- **Resume support** — identity-scoped segment-level resume, `.part` temp files, automatic re-extraction on expired URLs
- **Download archive** to skip already-downloaded videos
- **Stdout output** (`-o -`) for piping
- **Config file** support (YAML)

---

## Installation

```bash
go install github.com/tituscheng/ytgo@latest
```

Or build from source:

```bash
git clone https://github.com/tituscheng/ytgo.git
cd ytgo
go build -o ytgo .
```

**Dependencies:**
- [Go](https://go.dev/) 1.23+
- [FFmpeg](https://ffmpeg.org/) (optional, required for merge/audio extract/embed)

---

## Quick Start

```bash
# Download best quality
./ytgo "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

# List available formats
./ytgo --list-formats "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

# Audio-only MP3
./ytgo -x --audio-format mp3 --embed-thumbnail --embed-metadata "URL"

# Best video + audio, merge to MP4
./ytgo -f "bv*+ba/best" --merge-output-format mp4 "URL"

# Prefer H.264 + AAC in MP4 (type-safe — no regex DSL needed)
./ytgo --prefer-vcodec avc1 --prefer-acodec mp4a --prefer-ext mp4 "URL"

# Fetch likes alongside metadata (slower — secondary API call)
./ytgo --enrich-metadata --write-info-json --skip-download "URL"

# Download playlist with archive & subtitles
./ytgo --download-archive archive.txt \
       --write-subs --embed-subs --sub-langs en \
       "PLAYLIST_URL"

# Custom output template
./ytgo -o "%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s" "URL"

# Extract metadata only
./ytgo --write-info-json --write-description --skip-download "URL"

# Subtitles only (as SRT)
./ytgo --skip-download --write-auto-subs --sub-langs en --sub-format srt "URL"

# Pipe subtitle to stdout
./ytgo --skip-download --write-auto-subs --sub-langs en --sub-format vtt -o - "URL"
```

---

## Format Preferences

ytgo replaces yt-dlp's regex-based format filters (`vcodec~='^avc1'`) with **type-safe preference scoring**:

```bash
# CLI
./ytgo --prefer-vcodec avc1 --prefer-acodec mp4a --prefer-ext mp4 "URL"
```

```go
// Library
opts := api.DefaultOptions()
opts.Format = "bv+ba/best"
opts.PreferVideoCodec = "avc1"
opts.PreferAudioCodec = "mp4a"
opts.PreferContainer = "mp4"
```

Matching formats get a large score bonus (+5000) that outranks non-matching formats, so `best` naturally picks H.264/AAC/MP4 without any string DSL.

For edge cases, you can also provide a Go filter function:

```go
opts.FormatFilter = func(f ytgo.Format) bool {
    return f.Ext == "mp4" && f.Height >= 720
}
```

---

## Progress Callback

Instead of parsing yt-dlp's `[download] X.X%` stdout lines, ytgo exposes a structured callback:

```go
opts := api.DefaultOptions()
opts.OnProgress = func(downloaded, total int64) {
    pct := float64(downloaded) / float64(total) * 100
    fmt.Printf("Downloaded: %.1f%%\n", pct)
}
err := api.Download(ctx, url, opts)
```

For multi-format downloads (`bv+ba`), progress is **automatically aggregated** across all formats — you get a single callback showing total video progress.

When downloading with concurrent segment workers (`Workers > 1`), `OnProgress` still reports global byte progress against the full file size, not per-segment totals.

---

## Error Handling

For single videos, `api.Download()` returns the error directly. For playlists, per-item failures are reported via the `OnError` callback and via the returned `PlaylistReport` so you can track which videos failed without stopping the entire batch:

```go
opts := api.DefaultOptions()
var failures []ytgo.DownloadFailure
opts.OnError = func(f ytgo.DownloadFailure) {
    failures = append(failures, f)
    log.Printf("Failed [%s] at stage %s: %s (retryable: %v)",
        f.VideoID, f.Stage, f.Error, f.Retryable)
}
err := api.Download(ctx, playlistURL, opts)
// failures contains every failed video with full context
```

**CLI:** Write failures to a JSON file for later review:
```bash
./ytgo --write-error-log errors.json PLAYLIST_URL
```

---

## Get Stream URL

Resolve a temporary direct stream URL without downloading:

```go
result, err := api.GetStreamURL(ctx, api.GetStreamOptions{
    URL:    "https://www.youtube.com/watch?v=...",
    Format: "best[height<=1080]",
})
if err != nil {
    log.Fatal(err)
}

// result.URL       → direct playable URL
// result.Format    → full format metadata (codec, resolution, etc.)
// result.VideoInfo → full video metadata (title, thumbnail, etc.)

// With preferences and metadata enrichment:
result, err = api.GetStreamURL(ctx, api.GetStreamOptions{
    URL:              videoURL,
    Format:           "best",
    Enrich:           true,              // fetch LikeCount via secondary API call
    PreferVideoCodec: "avc1",            // boost H.264 score
    PreferAudioCodec: "mp4a",            // boost AAC score
    PreferContainer:  "mp4",             // boost MP4 score
    FormatFilter: func(f ytgo.Format) bool {
        return f.HasVideo && f.HasAudio // only combined formats
    },
})
```

This is better than yt-dlp's `--get-url` raw string because you get structured format and video metadata alongside the URL.

---

## Resume Support

ytgo has **segment-level resume** that is architecturally more robust than yt-dlp's single-file byte-counting:

| Feature | ytgo | yt-dlp |
|---|---|---|
| Granularity | Per-segment (bounded ~10 MB chunks) | Single-file byte offset |
| Temp file | `.part` → atomic rename on success | `.part` |
| Resume key | `(VideoID, FormatID, ContentLength)` — survives URL changes | File path only |
| URL expiry recovery | ✅ Re-extracts fresh URL on 403, continues | ❌ `.part` becomes useless |
| Integrity check | `clen=` from YouTube URL validates expected size | None |
| Periodic save | After every completed segment | Only on error/exit |
| Format-change safety | Discards stale state if `--format` changes | No protection |

**How it works:**
- Downloads write to `filename.part` with a `filename.part.segments` JSON sidecar tracking completed segments
- Interrupted downloads resume from the last completed segment, not from byte 0
- If the YouTube presigned URL expires (403), ytgo automatically re-extracts the video and continues with the fresh URL
- On success, the `.part` file is atomically renamed to the final name and the sidecar is removed

**CLI examples:**

```bash
# Default: resume is enabled. Interrupt with Ctrl+C and re-run — it continues.
./ytgo -f "best" "https://www.youtube.com/watch?v=..."

# Disable resume — start fresh even if a partial download exists
./ytgo --no-continue "URL"

# Skip existing files without re-downloading
./ytgo --no-overwrites "URL"
```

**Library example:**

```go
package main

import (
    "context"
    "log"

    "ytgo/pkg/ytgo/api"
)

func main() {
    opts := api.DefaultOptions()
    opts.Format = "best"
    opts.OutputTemplate = "%(title)s.%(ext)s"

    // Resume is enabled by default (ContinuePartial = true).
    // If the download is interrupted, re-run the same code and it resumes
    // from the last completed segment.
    err := api.Download(context.Background(), "URL", opts)
    if err != nil {
        log.Fatal(err)
    }

    // --- Optional flags ---

    // Force a fresh download even if a partial file exists
    opts.ContinuePartial = false

    // Skip already-downloaded files
    opts.NoOverwrites = true

    // The engine handles everything: .part temp files, .segments sidecars,
    // periodic saves, identity validation, and automatic re-extraction on 403.
}
```

---

## Configuration

ytgo reads a YAML config file from (in order of precedence):

1. `--config /path/to/config.yaml`
2. `./ytgo.yaml`
3. `~/.config/ytgo/ytgo.yaml`
4. `~/.ytgo/ytgo.yaml`

Example `~/.config/ytgo/ytgo.yaml`:

```yaml
format: "bv*+ba/best"
output: "%(title)s [%(id)s].%(ext)s"
audio-format: "mp3"
embed-metadata: true
embed-thumbnail: true
prefer-vcodec: "avc1"
prefer-ext: "mp4"
sub-langs:
  - en
```

---

## Architecture

```
┌─────────┐     ┌──────────┐     ┌────────────┐     ┌─────────────┐
│   URL   │────▶│ Extractor│────▶│   Format   │────▶│  Downloader │
└─────────┘     │ (YouTube)│     │  Selector  │     │  (HTTP +    │
                └──────────┘     └────────────┘     │   resume)   │
                                                     └──────┬──────┘
                                                            │
                                                     ┌──────▼──────┐
                                                     │ Postprocess │
                                                     │  (FFmpeg)   │
                                                     └─────────────┘
```

### Key Packages

| Package | Purpose |
|---|---|
| `internal/extractor` | `InfoExtractor` interface |
| `internal/extractor/youtube` | YouTube extractor wrapping the custom Innertube client |
| `internal/extractor/youtube/innertube` | Direct YouTube Innertube API client (ANDROID_VR / WEB_EMBEDDED_PLAYER) |
| `internal/downloader` | HTTP download with bounded chunk segmentation, concurrent workers, and resume |
| `internal/limiter` | Global rate limiter for downloads |
| `internal/pipeline` | Concurrent worker pool for extract/postprocess |
| `internal/format` | Format selection DSL parser |
| `internal/postprocessor` | FFmpeg-based merge/embed/convert |
| `internal/subtitle` | Subtitle fetch & JSON3→SRT/VTT conversion |
| `internal/template` | Output filename template engine |
| `internal/archive` | Download archive file I/O |
| `pkg/ytgo/api` | Public library API |

---

## Library Usage

See the **Resume Support** section above for a complete library example with resume options.

Quick start:

```go
package main

import (
    "context"
    "log"

    "ytgo/pkg/ytgo"
    "ytgo/pkg/ytgo/api"
)

func main() {
    opts := api.DefaultOptions()
    opts.Format = "best"
    opts.OutputTemplate = "%(title)s.%(ext)s"

    err := api.Download(context.Background(), "https://www.youtube.com/watch?v=...", opts)
    if err != nil {
        log.Fatal(err)
    }
}
```

### With progress callback

```go
opts := api.DefaultOptions()
opts.OnProgress = func(down, tot int64) {
    // Emit to your UI framework (Wails, Fyne, etc.)
}
err := api.Download(ctx, url, opts)
```

### Stream URL for playback

```go
result, err := api.GetStreamURL(ctx, api.GetStreamOptions{
    URL:    videoURL,
    Format: "best[height<=1080]",
})
// result.URL → stream URL
// result.Format.VideoCodec → "avc1.640028"
// result.VideoInfo.Title → "Video Title"
```

### Extract metadata only

```go
// Basic extraction (fast, single API call)
info, err := api.ExtractOnly(ctx, videoURL, 30*time.Second)

// Enriched extraction (slower, includes LikeCount)
info, err := api.Extract(ctx, api.ExtractOptions{
    URL:     videoURL,
    Timeout: 30 * time.Second,
    Enrich:  true,
})
// info.LikeCount → 12345
```

---

## Known Limitations

ytgo is YouTube-only and intentionally lean. Things yt-dlp does that ytgo does **not** yet support:

- **SponsorBlock** — no chapter-based ad skipping
- **Cookies from browser** — `--cookies-from-browser` is not implemented (cookie files work)
- **Other sites** — only YouTube (the `InfoExtractor` interface is ready for more)
- **Throttling bypass** — bounded chunk downloading handles most throttling; `ANDROID_VR` avoids signature-based throttling
- **String regex filters** — ytgo uses type-safe preference scoring and Go filter functions instead
- **Structured logging** — optional `*slog.Logger` injection for library users

See [`Future.md`](Future.md) for the roadmap.

---

## Adding New Sites

The `InfoExtractor` interface makes it trivial to add support for new platforms:

```go
type InfoExtractor interface {
    Name() string
    Suitable(url string) bool
    Extract(ctx context.Context, url string) (*ytgo.VideoInfo, error)
}
```

Register your extractor in the engine:

```go
engine := core.NewEngine(cfg)
engine.Register(myextractor.New())
```

---

## Development

```bash
# Run tests
go test ./...

# Run tests with race detector
go test -race ./...

# Run tests with coverage
go test -cover ./...

# Format code
gofmt -w .

# Vet code
go vet ./...
```

---

## License

MIT
