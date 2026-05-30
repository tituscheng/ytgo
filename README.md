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
- **Post-processing** via FFmpeg: merge, audio extraction (`-x`), metadata/thumbnail/chapter embedding. Safe concurrent execution (`--max-postprocessors`) with non-interleaved output and proper context cancellation.
- **Subtitles & Metadata Extraction**: Production-grade retry with exponential backoff + jitter for both subtitle tracks and core Innertube metadata extraction (Player/Playlist). Server `Retry-After` honored where applicable, atomic side-file writes, and structured failure reporting.
- **Output templates**: `%(title)s`, `%(upload_date>%Y-%m-%d)s`, `%(playlist_index)s`, etc.
- **Resume support** — identity-scoped segment-level resume, `.part` temp files, automatic re-extraction on expired URLs
- **Skip existing downloads** — by default, skips videos already present in the output directory (use `--no-skip-existing` to force re-download)
- **Download archive** to skip already-downloaded videos across runs (`--download-archive`)
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
# Download best quality (skips if already in output directory)
./ytgo "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

# Force re-download even when file exists
./ytgo --no-skip-existing "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

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

Instead of parsing yt-dlp's `[download] X.X%` stdout lines, ytgo exposes a single
structured callback that covers every phase — download, merge, and audio extraction:

```go
opts := api.DefaultOptions()
opts.OnProgress = func(p ytgo.Progress) {
    if f := p.Fraction(); f >= 0 {
        fmt.Printf("[%s] %s: %.1f%%\n", p.Phase, p.Title, f*100)
    }
}
err := api.Download(ctx, url, opts)
```

`Progress.Fraction()` returns completion in `[0,1]`, or `-1` when the total isn't
known yet (render an indeterminate indicator rather than a bar). `Cur`/`Tot` are
opaque units that depend on the phase — bytes for `PhaseDownload`, milliseconds of
media time for `PhaseMerge` and `PhaseAudio` — but most consumers only need
`Fraction()` and `Phase`.

The engine **serializes** calls to `OnProgress`, so your callback does not need to
be safe for concurrent use even during concurrent playlist downloads or
post-processing.

Identity is carried on every event: for multi-format downloads (`bv+ba`) you get one
event per `FormatID`, all sharing the same `VideoID` — aggregate by `VideoID` if you
want a single per-video number. When downloading with concurrent segment workers
(`Workers > 1`), download events report global byte progress against the full file
size, not per-segment totals.

---

## Error Handling

For single videos, `api.Download()` returns the error directly. For playlists, per-item failures are reported via the `OnError` callback and via the returned `PlaylistReport` so you can track which videos failed without stopping the entire batch:

```go
opts := api.DefaultOptions()
var failures []ytgo.DownloadFailure
opts.OnError = func(f ytgo.DownloadFailure) {
    // Calls are serialized by the engine — no caller-side mutex needed.
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

# Skip is enabled by default when a video ID is already present in the output directory.
# Include %(id)s in -o for reliable detection, or rely on the exact output path for custom templates.
./ytgo --no-skip-existing "URL"   # force re-download
```

**Library example:**

```go
package main

import (
    "context"
    "log"

    "github.com/tituscheng/ytgo/pkg/ytgo/api"
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
| `internal/subtitle` | Subtitle fetch (retry/backoff, atomic writes) & JSON3→SRT/VTT conversion |
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

    "github.com/tituscheng/ytgo/pkg/ytgo"
    "github.com/tituscheng/ytgo/pkg/ytgo/api"
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
opts.OnProgress = func(p ytgo.Progress) {
    // Emit p.Phase / p.Title / p.Fraction() to your UI framework (Wails, Fyne, etc.)
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
- **Cookies from browser** — browser cookie extraction was never wired; the `--cookies-from-browser` (and related networking) flags have been removed. Cookie file support via the library API remains available for authenticated scenarios.
- **Other sites** — only YouTube (the `InfoExtractor` interface is ready for more)
- **Throttling bypass** — bounded chunk downloading handles most throttling; `ANDROID_VR` avoids signature-based throttling
- **String regex filters** — ytgo uses type-safe preference scoring and Go filter functions instead
- **Structured logging** — optional `*slog.Logger` injection for library users

See [`Future.md`](Future.md) for the roadmap.

### Reliability & Production Notes

- **Concurrent post-processing** (`--max-postprocessors > 1`) now produces clean, non-interleaved FFmpeg output.
- **Metadata extraction** (Innertube) now has the same retry quality previously reserved for subtitles.
- **`--no-overwrites`** consistently protects the main media file *and* all side files (`.info.json`, `.description`, thumbnails).
- **Large playlists** are protected by a defensive upper bound (50k entries) to prevent pathological memory usage.
- Context cancellation is respected across multi-format downloads and post-processing stages.

---

## Known Limitations

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
