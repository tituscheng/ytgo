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
- **HTTP download** with bounded chunk segmentation (~10 MB), concurrent workers, resume support, and progress spinners
- **Post-processing** via FFmpeg: merge, audio extraction, metadata/thumbnail/chapter embedding
- **Subtitles**: download manual & auto-generated captions, convert JSON3 → SRT/VTT
- **Output templates**: `%(title)s`, `%(upload_date>%Y-%m-%d)s`, `%(playlist_index)s`, etc.
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

    err := api.Download(context.Background(), "https://www.youtube.com/watch?v=...", opts)
    if err != nil {
        log.Fatal(err)
    }
}
```

---

## Known Limitations

ytgo is YouTube-only and intentionally lean. Things yt-dlp does that ytgo does **not** yet support:

- **SponsorBlock** — no chapter-based ad skipping
- **Cookies from browser** — `--cookies-from-browser` is not implemented (cookie files work)
- **Other sites** — only YouTube (the `InfoExtractor` interface is ready for more)
- **Throttling bypass** — bounded chunk downloading handles most throttling; `ANDROID_VR` avoids signature-based throttling
- **Full format selection DSL** — covers the common cases, not every yt-dlp edge case

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
