# Architecture

This document explains how ytgo is structured, why it is structured that way, and how the pieces fit together.

---

## Design Philosophy

1. **Layered, not framework-y.** Each package has a single, well-defined job. The `core` package wires them together, but nothing else depends on `core`.
2. **Interface-driven.** The `InfoExtractor` interface means YouTube is just one plugin. Adding Vimeo or TikTok is a matter of implementing three methods.
3. **No JS engine, no Python runtime.** The custom Innertube client gets direct stream URLs without executing JavaScript or deciphering signatures. This is the main reason ytgo is smaller and faster than yt-dlp.
4. **Context-first.** Every long-running operation accepts a `context.Context` for cancellation and timeouts.

---

## High-Level Flow

```
CLI (cobra/viper) → Config → Engine → Extractor → Format Selector → Downloader → Post-processor
                                            ↓
                                   Library API (pkg/ytgo/api)
```

A URL enters through **one of two doors:**

- **`cmd/root.go`** — the CLI path. Parses flags, reads config files, builds a `core.Engine`, and runs it.
- **`pkg/ytgo/api`** — the library path. A thin wrapper that does the same engine setup for programmatic use.

Both paths converge on `core.Engine.Run()`, which is the single entry point for the download pipeline.

---

## Layer Breakdown

### 1. CLI Layer (`cmd/`)

**File:** `cmd/root.go`

- Uses **spf13/cobra** for command-tree and flag parsing.
- Uses **spf13/viper** for config-file layering (flag > env > file > default).
- Reads `ytgo.yaml` from `./`, `~/.config/ytgo/`, or `~/.ytgo/`.
- Builds a `core.Engine`, registers the YouTube extractor, and calls `engine.Run(ctx, url)`.
- Sets up `signal.NotifyContext` so Ctrl-C cancels the whole pipeline cleanly.

**Why cobra + viper?** They are the de-facto standard in Go CLI tooling. Cobra gives us help text, shell completion, and subcommand support for free. Viper gives us the exact precedence rules yt-dlp users expect.

---

### 2. Public API Layer (`pkg/ytgo/api`)

**File:** `pkg/ytgo/api/api.go`

- Re-exports `config.DownloadOptions` as `api.DownloadOptions`.
- `api.Download(ctx, url, opts)` — one-shot download.
- `api.Extract(ctx, opts)` — metadata extraction with `ExtractOptions` (supports enrichment).
- `api.ExtractOnly(ctx, url, timeout)` — convenience wrapper for basic metadata extraction.

**Why a separate package?** `pkg/` is the conventional Go location for public library surface. Internal packages can still change; `pkg/ytgo/api` is the stability contract.

---

### 3. Engine Layer (`internal/core/`)

**File:** `internal/core/engine.go`

The `Engine` struct is the orchestrator. It owns:

- A slice of `extractor.InfoExtractor` implementations.
- A `*downloader.Downloader`.
- A `config.DownloadOptions`.

**Pipeline stages:**

```
Run(url)
  1. Find suitable extractor (first match on Suitable(url))
  2. Extract metadata → *VideoInfo
  3. If playlist → runPlaylist() (iterate entries, re-extract each)
  4. If video    → runVideo()
       a. Check download archive
       b. Select formats
       c. Download each format
       d. Merge (if multiple formats)
       e. Extract audio (if -x)
       f. Embed metadata/thumbnail/subs/chapters
       g. Write side files (info.json, description, thumbnail)
       h. Write subtitles
       i. Record in archive
```

**Key design decisions:**

- **Playlist entries are re-extracted individually.** The Innertube `browse` endpoint returns metadata (title, duration, thumbnails) but not format URLs. Each entry gets a fresh `player` request before download. This is why playlist extraction is fast (~0.4 s for 15 items) compared to yt-dlp, which does a full player request per entry during the list phase.
- **Stdout output (`-o -`) is handled by downloading to a temp file, then streaming it to `os.Stdout`.** This avoids trying to pipe an in-progress HTTP download directly, which complicates merge and post-process.
- **Archive check happens before format selection.** If the video is already archived, we skip everything — no network requests, no format parsing.

---

### 4. Extraction Layer (`internal/extractor/`)

#### Interface (`internal/extractor/extractor.go`)

```go
type InfoExtractor interface {
    Name() string
    Suitable(url string) bool
    Extract(ctx context.Context, url string) (*VideoInfo, error)
}
```

- `VideoInfo`, `Format`, `Subtitle`, etc. are re-exports from `pkg/ytgo` so internal packages don't depend on `pkg` directly.

#### YouTube Extractor (`internal/extractor/youtube/`)

**Files:** `extractor.go`, `innertube/*.go`

The YouTube extractor is a thin adapter over the custom Innertube client:

```
URL ──▶ youtube.Extractor.Extract()
            ├── video URL ──▶ innertube.Client.Player() ──▶ map to VideoInfo
            └── playlist URL ──▶ innertube.Client.Playlist() ──▶ map to VideoInfo with Entries
```

**Why a custom Innertube client instead of kkdai/youtube/v2?**

| Aspect | kkdai/youtube/v2 | Custom innertube |
|---|---|---|
| JS engine | Embedded goja (V8 in Go) | None |
| Binary size | ~+10 MB | ~0 MB |
| Player client | WEB (requires sig deciphering) | ANDROID_VR (direct URLs) |
| Age-restricted | Often fails | WEB_EMBEDDED_PLAYER fallback |
| Playlist | Basic | Full continuation pagination |

The custom client was built because kkdai's WEB client requires JavaScript execution to decipher stream signatures. ANDROID_VR returns pre-signed direct URLs — no JS needed. The trade-off is that ANDROID_VR sometimes returns `LOGIN_REQUIRED` for age-restricted content, so we fall back to `WEB_EMBEDDED_PLAYER`.

#### Innertube Client (`internal/extractor/youtube/innertube/`)

**Files:** `client.go`, `player.go`, `playlist.go`, `types.go`

- **`client.go`** — HTTP client with visitor ID management.
  - `refreshVisitorID()` fetches `youtube.com` homepage HTML and parses `ytcfg.set(...INNERTUBE_CONTEXT...)` to get a real `visitorData` value. YouTube bot detection rejects randomly generated visitor data.
  - `randomVisitorData()` generates protobuf-encoded fallback visitor data matching kkdai's format.
  - Request building: JSON POST to `/youtubei/v1/player` or `/youtubei/v1/browse` with `X-Youtube-Client-Name`, `X-Youtube-Client-Version`, and `x-goog-visitor-id` headers.

- **`player.go`** — Video metadata extraction.
  - Primary: `ANDROID_VR` client (client name 3, version 1.65.10). No API key. Returns direct URLs.
  - Fallback: `WEB_EMBEDDED_PLAYER` on `LOGIN_REQUIRED` status.
  - Error on `ERROR` status or private videos.

- **`playlist.go`** — Playlist metadata extraction.
  - POST to `/youtubei/v1/browse` with the playlist ID as `browseId`.
  - Parses both `singleColumnBrowseResultsRenderer` and `twoColumnBrowseResultsRenderer` layouts.
  - Follows continuation tokens (`continuationItems`) until exhausted.
  - Each `PlaylistVideoItem` maps to a `PlaylistEntry` with ID, Title, Author, Duration, Thumbnails.

- **`types.go`** — JSON struct definitions for all Innertube responses.
  - `PlayerResponse`, `PlaylistResponse`, `ContinuationResponse`.
  - `Text`/`Run` helpers for YouTube's renderer-based text format.

---

### 5. Format Selection Layer (`internal/format/`)

**File:** `internal/format/selector.go`

Parses yt-dlp-style format selectors:

- `best`, `worst`, `bestvideo`, `bestaudio`
- Itag codes: `18`, `137`
- Extension: `mp4`, `webm`
- Resolution filters: `best[height<=720]`
- Merge syntax: `bv*+ba/best` (best video + best audio, fallback to best combined)

**Preference scoring** (`SelectWithOptions`):
- `PreferVideoCodec`, `PreferAudioCodec`, `PreferContainer` add +5000 to the heuristic score
- This outranks non-matching formats without excluding them
- A `FormatFilter func(Format) bool` pre-filter can hard-exclude formats

Returns a slice of `extractor.Format` to download. The engine downloads each format in the slice, then merges if there are multiple.

---

### 6. Download Layer (`internal/downloader/`)

**Files:** `downloader.go`, `segment.go`, `planner.go`, `resume.go`

A segmented HTTP downloader with **resume support**, **bounded chunk sizes**, and optional **concurrent segment fetching**.

```go
d := downloader.New()
d.Progress = func(down, total int64) { /* update spinner */ }
err := d.DownloadToFile(ctx, url, "/path/to/video.mp4")
```

**Progress aggregation:** When the caller sets `config.OnProgress` and multiple formats are selected (`bv+ba`), the engine aggregates per-format progress into a single callback via `progressAggregate`.

- **Bounded chunks:** All requests use `Range: bytes=N-M` with a maximum chunk size of ~10 MB. YouTube's CDN throttles unbounded ranges (`bytes=0-`) to ~32 KB/s on some videos.
- **Segmented downloads:** Files are split into chunks. With `Workers > 1`, chunks are downloaded concurrently via `errgroup.Group`. With `Workers == 1`, chunks are downloaded sequentially.
- **Resume support:** A sidecar JSON file (`{dest}.segments`) tracks completed segments. Interrupted downloads resume from the last missing chunk.
- **Identity-scoped resume:** `ResumeState` keys on `(VideoID, FormatID, ContentLength)`, not URL. Changing `--format` between runs automatically discards stale state. YouTube URL expiry is handled by re-extraction on 403.
- **Periodic save:** The sidecar is flushed to disk after every completed segment, so a crash loses at most one segment's worth of work.
- **`.part` temp files:** Downloads write to `filename.part` and are atomically renamed to the final name on success. Incomplete downloads never look like complete files.
- **Direct I/O:** `SegmentDownloader` uses `WriteAt` to write chunks directly to the correct file offset without temporary fragment files.
- **Chunk planning:** `PlanSegments(totalSize, maxWorkers, minChunkSize, maxChunkSize)` balances concurrency against chunk size, never exceeding the 10 MB bound.

**Why bounded chunks?** YouTube's CDN applies different throttling rules based on Range request size. Unbounded or very large (> ~15 MB) ranges are throttled to ~32 KB/s. Bounded chunks ≤ 10 MB stream at full bandwidth. This behavior is video-dependent — some videos allow unbounded ranges, others do not. The bounded-chunk strategy is the safe, universal approach.

---

### 7. Post-Processing Layer (`internal/postprocessor/`)

**Files:** `postprocessor.go`, `thumbnail.go`

FFmpeg-based post-processing:

- **`Merger`** — combines separate video + audio files into one container.
- **`Converter`** — extracts audio (`-x --audio-format mp3`).
- **`Embedder`** — embeds metadata, thumbnail, subtitles, chapters into the output file.
- **`DownloadThumbnail`** — fetches the best thumbnail URL and saves it.

**Auto-faststart:** MP4/M4A/MOV outputs automatically receive `-movflags +faststart` for web streaming compatibility. No flag required.

All post-processors accept the path to the `ffmpeg` binary via `config.FFmpegLocation`.

---

### 8. Supporting Packages

| Package | Responsibility |
|---|---|
| `internal/config` | `DownloadOptions` struct with `mapstructure` tags for viper unmarshalling |
| `internal/archive` | Plain-text ID archive (one video ID per line) |
| `internal/template` | Output filename template engine: `%(title)s`, `%(id)s`, `%(upload_date>%Y-%m-%d)s`, etc. |
| `internal/subtitle` | Fetch subtitle tracks, convert JSON3 → SRT/VTT |
| `pkg/ytgo` | Shared domain types: `VideoInfo`, `Format`, `Subtitle`, `Thumbnail`, `Chapter` |

---

## Data Model

```go
// pkg/ytgo
type VideoInfo struct {
    ID, Title, Description, Uploader, UploaderID string
    Duration                                     int
    UploadDate                                   time.Time
    WebpageURL, OriginalURL                      string
    Formats                                      []Format
    Subtitles                                    []Subtitle
    Thumbnails                                   []Thumbnail
    Chapters                                     []Chapter
    // Playlist fields
    Playlist, PlaylistID, PlaylistTitle          string
    PlaylistIndex, PlaylistCount                 int
    Entries                                      []*VideoInfo
}

type Format struct {
    FormatID, URL, Ext                           string
    Width, Height, Bitrate, Filesize             int
    VideoCodec, AudioCodec, QualityLabel         string
    FPS, AudioChannels                           int
    // ...
}
```

A `VideoInfo` can represent either a single video or a playlist. If `len(Entries) > 0`, it is a playlist. Each entry is itself a `*VideoInfo`.

---

## Extension Points

### Adding a New Extractor

1. Implement `extractor.InfoExtractor`.
2. Register it in `cmd/root.go` (for CLI) or `pkg/ytgo/api` (for library):

```go
engine := core.NewEngine(cfg)
engine.Register(myextractor.New())
```

### Adding a New Post-Processor

Post-processors are plain structs with a `Run` method. The engine calls them in sequence. Add a new step to `runVideo()` in `internal/core/engine.go`.

### Adding a New Format Selector

Extend `internal/format/selector.go`. The selector already understands most yt-dlp syntax; adding new filters (e.g. `vcodec=av01`) is a matter of adding a predicate function.

---

## Testing Strategy

- **Unit tests** with `httptest` for the Innertube client (`innertube_test.go`). Mock JSON responses, assert parsed structs.
- **No network-dependent tests in CI.** All extractor tests use mocked HTTP.
- **Manual integration tests** against real YouTube URLs for regression checking (not committed).

---

## Dependency Philosophy

ytgo keeps its dependency tree minimal:

| Dependency | Why |
|---|---|
| `spf13/cobra` | CLI framework (flags, help, completion) |
| `spf13/viper` | Config file + env var layering |
| `fatih/color` | Terminal colors |
| `briandowns/spinner` | Progress indicators |
| `stretchr/testify` | Test assertions (dev only) |

**Notably absent:**
- No JavaScript engine (no `goja`, no `duktape`)
- No JSON parser beyond `encoding/json`
- No HTTP framework beyond `net/http`

This is how the binary stays at ~11 MB.
