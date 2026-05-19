# ytgo Performance Architecture Plan

## Vision: Not "yt-dlp in Go" — A Next-Generation Downloader

yt-dlp is a 20-year-old Python architecture carrying massive technical debt: Python GIL, serial playlist processing, regex-based JS interpretation, unbounded memory growth, and no true pipeline overlap. ytgo's clean Go codebase gives us a once-in-a-decade chance to **leapfrog** rather than catch up.

The core insight: **A downloader is a dataflow problem, not a file-transfer problem.** Go's goroutines + channels make pipeline architectures trivial. Python's GIL makes them nearly impossible. This is ytgo's structural advantage.

---

## Part 1: What yt-dlp Got Wrong — Lessons Learned

### 1. The GIL Prison
yt-dlp's `YoutubeDL` class is fundamentally single-threaded. Parallel playlist downloading is considered "architecturally infeasible" (Issue #11909). An entire ecosystem of wrapper tools (`yt-dlpp`, `yt-dlp-playlist-parallelizer`) exists solely to work around this.

**Lesson:** Design for concurrency from day one. Every stage must be goroutine-safe and independently scalable.

### 2. Memory as Unbounded Heap
yt-dlp holds all playlist metadata in memory before beginning downloads (Issue #13946: 2.4 GB for large channels). Circular references prevent Python GC from freeing extractor objects (Issue #1949).

**Lesson:** Stream playlist entries. Don't materialize entire playlists. Use bounded channels as backpressure.

### 3. The Regex JS Interpreter Disaster
yt-dlp maintained a regex-based JavaScript "interpreter" for YouTube signature deciphering for years. In 2025, maintainers admitted it was "hugely costly to maintain" and abandoned it for an external JS runtime.

**Lesson:** ytgo's ANDROID_VR approach (no JS needed) is already the right call. Never add a JS engine.

### 4. Serial Download → Post-Process Chain
yt-dlp downloads a file fully, then runs FFmpeg, then moves to the next file. Network and CPU are never saturated simultaneously (Issue #1918, open since 2021).

**Lesson:** Overlap download (network-bound) with post-processing (CPU/disk-bound). This alone yields 1.5–2x real-world speedup on multi-core machines.

### 5. Fragment-Level Only Concurrency
yt-dlp's `-N` flag only parallelizes DASH/HLS fragments. Direct HTTP URLs are downloaded single-stream. This means YouTube's direct `ANDROID_VR` URLs get no parallelism at all.

**Lesson:** Implement **segmented single-file downloads** via HTTP Range requests for *all* URLs, not just manifests. This is what aria2c does — and yt-dlp doesn't.

### 6. Three Separate FFmpeg Invocations
For a typical `bv+ba` + audio extraction + embed workflow, yt-dlp spawns FFmpeg 3 times, each re-muxing the entire container.

**Lesson:** Coalesce FFmpeg stages where possible. Even better: stream directly through FFmpeg for audio extraction, eliminating intermediate files entirely.

---

## Part 2: The Innovative Architecture — "Saturation Pipeline"

### Core Design: Three-Stage Pipeline with Bounded Concurrency

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│  EXTRACTOR POOL │────▶│  DOWNLOAD POOL  │────▶│  POST-PROC POOL │
│  (Metadata)     │     │  (Segments)     │     │  (FFmpeg)       │
│  concurrency=E  │     │  concurrency=D  │     │  concurrency=P  │
└─────────────────┘     └─────────────────┘     └─────────────────┘
        │                       │                       │
        ▼                       ▼                       ▼
   Bounded chan              Bounded chan             Bounded chan
   capacity=E                capacity=D               capacity=P
```

- **Extractor Pool:** E goroutines fetch metadata concurrently. Playlist entries stream through — never buffered entirely.
- **Download Pool:** D goroutines handle downloads. Each download internally uses **segmented parallelism** (see below).
- **Post-Process Pool:** P goroutines run FFmpeg. Overlaps with downloading of the *next* video.

**Key invariant:** While video N is being post-processed, video N+1 can be downloading, and video N+2 can be extracting. **All three stages run concurrently.**

### Innovation 1: Dynamic Segment Saturation (DSS)

Instead of a fixed `-N` connection count (yt-dlp) or manual `-x16` (aria2c), ytgo **probes and adapts**:

1. Start with 2 parallel connections/segments.
2. Measure aggregate throughput every 500 ms.
3. Add a connection if marginal gain > 15%.
4. Remove a connection if marginal gain < 5% or if server returns 429/503.
5. Adapt continuously throughout the download.

This is **application-level congestion control**. It avoids the two failure modes of fixed counts:
- **Too few connections:** Under-utilizing a fast link.
- **Too many connections:** Triggering server rate limits or TCP congestion collapse.

**Implementation:** A `SegmentPlanner` goroutine per download that manages a pool of `SegmentFetcher` goroutines. A `ThroughputProbe` samples bytes/sec and signals scale-up/scale-down via channel.

### Innovation 2: Ring-Buffer Segment Assembly (Zero-Merge)

For segmented downloads:

```
Segment Fetcher 1 ──►┌─────────────┐
Segment Fetcher 2 ──►│ Ring Buffer │──► Assembler ──► pwrite(fd, offset)
Segment Fetcher 3 ──►│  (bounded)  │      (orders segments)
Segment Fetcher N ──►└─────────────┘
```

- Ring buffer has 8–16 slots. Each slot holds one segment's data.
- Assembler pulls segments in order and writes directly to the final file at the correct byte offset via `pwrite()` or `mmap`.
- **No temp fragment files. No merge step.** The final file is assembled in-place.
- If disk is slower than network, the ring fills → fetchers block naturally (backpressure).
- Resume: a `.segments` JSON file tracks completed byte ranges. On restart, only missing ranges are fetched.

**For `bv+ba` downloads:** Both streams download concurrently via separate segmented fetches. They are assembled into separate temp files (still needed for FFmpeg merging), but the download itself is fully parallel.

### Innovation 3: Lazy Post-Processing Stream

For audio extraction (`-x`), eliminate the intermediate file entirely:

```
HTTP Response Body ──► [FFmpeg stdin pipe] ──► demux ──► decode ──► encode ──► output.mp3
```

Go's `io.Pipe()` makes this elegant:
- Downloader writes to the pipe's `Writer`.
- FFmpeg reads from the pipe's `Reader` via `-i pipe:0`.
- Single-pass streaming. No `.fXXX.m4a` temp file.

**For merge + embed:** Where possible, combine into one FFmpeg invocation:
```bash
ffmpeg -i video -i audio -i thumbnail -c copy -metadata title="..." -disposition:v:1 attached_pic output.mp4
```

### Innovation 4: HTTP/2 Range Stream Multiplexing

On HTTP/2 servers (Google Video, CloudFront), open **one connection** and issue multiple `Range` requests as independent streams:

```go
// Single HTTP/2 connection
req1, _ := http.NewRequest("GET", url, nil)
req1.Header.Set("Range", "bytes=0-1048575")
req2, _ := http.NewRequest("GET", url, nil)
req2.Header.Set("Range", "bytes=1048576-2097151")
// Both sent concurrently on the same connection
```

- Reduces TLS handshake overhead vs. multiple HTTP/1.1 connections.
- Exploits HTTP/2's 100+ concurrent stream limit.
- Falls back to HTTP/1.1 multiple connections if HTTP/2 is unavailable.

### Innovation 5: Predictive Prefetching

While the **Download Pool** is busy with video N, the **Extractor Pool** can prefetch metadata for videos N+1, N+2, etc. Since ytgo's extraction is already ~0.4s, this means the pipeline is **never stalled waiting for metadata**.

For playlists: begin extracting the next batch of entries while the current batch downloads.

---

## Part 3: Concrete Implementation Plan

### Phase 1: Foundation — The Concurrency Core

**Goal:** Replace the serial engine with a pipelined, concurrent engine. No segmented downloads yet — just parallelize what exists.

#### New Files

- `internal/pipeline/pipeline.go` — Core pipeline orchestrator.
  ```go
  type Pipeline struct {
      ExtractorPool  *WorkerPool
      DownloadPool   *WorkerPool
      PostProcPool   *WorkerPool
      Transport      *http.Transport
      GlobalLimiter  *rate.Limiter  // golang.org/x/time/rate
      BufferPool     *sync.Pool
  }
  type VideoJob struct {
      URL         string
      Info        *extractor.VideoInfo
      Stage       Stage // extract | download | postproc | done
      Err         error
  }
  ```

- `internal/pipeline/workerpool.go` — Generic bounded worker pool using `errgroup.Group` + semaphore.
  ```go
  type WorkerPool struct {
      limit int
      sem   chan struct{}
      eg    *errgroup.Group
  }
  func (wp *WorkerPool) Submit(ctx context.Context, fn func() error) error
  ```

- `internal/transport/tuned.go` — Shared, tuned `http.Transport`.
  ```go
  func NewTunedTransport() *http.Transport {
      return &http.Transport{
          MaxIdleConns:        100,
          MaxIdleConnsPerHost: 10,
          IdleConnTimeout:     90 * time.Second,
          ForceAttemptHTTP2:   true,
          DisableCompression:  false,
      }
  }
  ```

#### Modified Files

- `internal/core/engine.go` — Rewrite `Run()` to enqueue jobs into the pipeline instead of serial loops.
- `cmd/root.go` — Add `--max-downloads`, `--max-extractors`, `--max-postprocessors` flags.

**Deliverable:** Playlist entries download concurrently. `bv+ba` formats download concurrently. No intermediate file elimination yet.

---

### Phase 2: The Segment Downloader

**Goal:** Replace the single-stream `Downloader` with a segmented, dynamically-saturating downloader.

#### New Files

- `internal/downloader/segment.go` — Segment planner and fetcher.
- `internal/downloader/planner.go` — Dynamic Segment Saturation (DSS) engine.
- `internal/downloader/ring.go` — Bounded ring buffer for segment data.
- `internal/downloader/assembler.go` — Ordered segment writer.
- `internal/downloader/resume.go` — Segment-granular resume state.

#### Modified Files

- `internal/downloader/downloader.go` — Retain as thin wrapper. Dispatch to `SegmentDownloader` if `Accept-Ranges` supported, else fall back to single-stream.

**Deliverable:** All HTTP downloads use segmented parallelism with dynamic connection counts. Resume works at segment granularity.

---

### Phase 3: Pipeline Overlap

**Goal:** While video N post-processes, video N+1 downloads, and video N+2 extracts.

#### Modified Files

- `internal/pipeline/pipeline.go` — Wire the three stages with bounded channels.
- `internal/core/engine.go` — `runPlaylist()` submits jobs to pipeline.
- `internal/postprocessor/` — Ensure goroutine safety.

**Deliverable:** Full three-stage pipeline. CPU (FFmpeg) and network are saturated simultaneously. Memory stays bounded.

---

### Phase 4: Streaming Post-Process

**Goal:** Eliminate intermediate files for audio extraction and simplify merge+embed.

#### New Files

- `internal/postprocessor/stream.go` — Streaming FFmpeg wrapper via `io.Pipe()`.

#### Modified Files

- `internal/postprocessor/postprocessor.go` — Add `CoalescedEmbed()` for one-shot merge+embed.
- `internal/core/engine.go` — Use streaming converter for `-x` flag.

**Deliverable:** Audio extraction is single-pass (no temp file). Merge+embed is one FFmpeg invocation where possible.

---

### Phase 5: Advanced Networking & Polish

**Goal:** HTTP/2 multiplexing, rate limiting, progress aggregation, and benchmarking.

#### New Files

- `internal/downloader/h2mux.go` — HTTP/2 range stream multiplexer.
- `internal/progress/hub.go` — Centralized, goroutine-safe progress aggregator.
- `internal/limiter/global.go` — Shared rate limiter.

#### Benchmarking Plan

- `internal/downloader/downloader_bench_test.go`
  - `BenchmarkSingleStream` (baseline)
  - `BenchmarkSegmented` (with DSS)
  - `BenchmarkPlaylistSerial` (old)
  - `BenchmarkPlaylistPipeline` (new)
- CI target: Segment downloader ≥ 2x faster than single-stream on ≥ 100 Mbps links.

---

## Part 4: File-by-File Change Summary

| File | Action | Description |
|------|--------|-------------|
| `internal/pipeline/pipeline.go` | **New** | Three-stage pipeline orchestrator |
| `internal/pipeline/workerpool.go` | **New** | Bounded errgroup-based worker pool |
| `internal/transport/tuned.go` | **New** | Shared HTTP/2-tuned transport |
| `internal/downloader/segment.go` | **New** | Segmented download orchestrator |
| `internal/downloader/planner.go` | **New** | Dynamic Segment Saturation (DSS) |
| `internal/downloader/ring.go` | **New** | Bounded ring buffer for assembly |
| `internal/downloader/assembler.go` | **New** | Ordered pwrite assembler |
| `internal/downloader/resume.go` | **New** | Segment-granular resume state |
| `internal/downloader/h2mux.go` | **New** | HTTP/2 range stream multiplexer |
| `internal/postprocessor/stream.go` | **New** | Streaming FFmpeg via io.Pipe |
| `internal/progress/hub.go` | **New** | Goroutine-safe progress aggregator |
| `internal/limiter/global.go` | **New** | Global rate limiter |
| `internal/core/engine.go` | **Rewrite** | Pipeline-based `Run()`, `runPlaylist()`, `runVideo()` |
| `internal/downloader/downloader.go` | **Modify** | Thin wrapper dispatching to segment or single-stream |
| `cmd/root.go` | **Modify** | New flags: `--max-downloads`, `--max-extractors`, `--max-postprocessors`, `--limit-rate` |
| `internal/config/config.go` | **Modify** | Add `MaxDownloads`, `MaxExtractors`, `MaxPostProcessors`, `LimitRate` |
| `internal/archive/archive.go` | **Modify** | In-memory cache with `sync.RWMutex` |

---

## Part 5: Key Design Decisions & Trade-offs

### 1. Why `pwrite` / `mmap` instead of temp files?
**Decision:** Use `pwrite()` for segment assembly. `mmap` as future optimization.
**Trade-off:** `pwrite` is portable (works on all platforms), eliminates merge I/O, but requires the final file size to be known (or pre-allocated). For unknown sizes, fall back to append-mode assembly.

### 2. Why channels + `errgroup` instead of `sync.WaitGroup`?
**Decision:** `golang.org/x/sync/errgroup` with semaphore for worker pools.
**Trade-off:** Slightly more overhead than raw `WaitGroup`, but gives us **cancellation propagation** and **first-error termination** for free. Essential for a pipeline where one failure should drain the rest cleanly.

### 3. Why not use an external downloader (aria2c)?
**Decision:** Implement segmented downloads natively in Go.
**Trade-off:** More code than shelling out to aria2c, but gives us **dynamic connection probing**, **streaming post-processing**, and **tight progress integration** that external tools cannot provide.

### 4. Why bounded channels instead of unbounded queues?
**Decision:** Every inter-stage channel has capacity = worker count of the receiving stage.
**Trade-off:** If a stage is slow (e.g., FFmpeg throttled), the previous stage blocks. This is **intentional backpressure** — it prevents unbounded memory growth (yt-dlp's fatal flaw).

### 5. Why one shared `http.Transport`?
**Decision:** Single tuned transport shared across extractors, downloaders, and thumbnail fetchers.
**Trade-off:** All connections share one pool. But HTTP/2 multiplexing means one transport can handle hundreds of concurrent streams. Simpler and more efficient than per-component clients.

### 6. How to handle YouTube rate limits with parallelism?
**Decision:** DSS naturally throttles down when it detects 429/503. Additionally, cap per-host concurrent connections (default 5 for YouTube).
**Trade-off:** We may be slightly more conservative than aria2c's fixed `-x16`, but we avoid IP bans. The probing algorithm finds the actual sustainable limit.

---

## Part 6: Success Metrics

| Metric | Baseline (Current ytgo) | Target |
|--------|------------------------|--------|
| Playlist 50 videos | ~50× (single video time) | ~5× (10× speedup via parallelism) |
| `bv+ba` download | Sequential (video then audio) | Concurrent (both simultaneously) |
| Single large file | 1 connection | 3–8 dynamic connections, ≥2× throughput |
| Audio extraction (`-x`) | Download → temp file → FFmpeg | Stream directly, no temp file |
| Memory (100-video playlist) | Linear growth | Bounded by pipeline capacity |
| Resume granularity | File-level | Segment-level (byte ranges) |

---

## Appendix A: Deferred Plans

### Hybrid n-sig / Signature Cipher Solver

**Status:** ⏸️ Deferred — not currently needed.

**Original plan:** A native Go signature cipher solver + Node.js n-sig worker to handle YouTube's JavaScript challenge solving, similar to yt-dlp's approach.

**Why it was deferred:**
After implementing bounded chunk downloading, we discovered that the throttling was **not** caused by missing n-sig solving. The actual root cause was YouTube's CDN throttling unbounded `Range: bytes=0-` requests. The bounded chunk fix achieves full download speed (~16 MB/s) without any JavaScript runtime.

**Current evidence we don't need it:**
- The ANDROID_VR client returns pre-signed direct URLs with no `n` parameter
- yt-dlp on this machine also skips n-sig solving (Deno/EJS components unavailable) yet downloads successfully via the same ANDROID_VR client
- All tested videos (normal and long-form) download at full speed with bounded chunks alone

**When to revisit:**
- If YouTube changes ANDROID_VR to return URLs requiring signature deciphering
- If age-restricted videos (WEB_EMBEDDED_PLAYER fallback) fail or throttle due to ciphered formats
- If a specific video is found that requires n-sig solving even with bounded chunks

**Plan location:** `~/.kimi/plans/taskmaster-valkyrie-superboy.md` (approved, implementation-ready)

---

## Appendix B: Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                           CLI / Library                              │
│                         cmd/root.go, pkg/ytgo/api                    │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         Pipeline Orchestrator                        │
│                    internal/pipeline/pipeline.go                     │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────────┐ │
│  │ Extractors  │───▶│  Download   │───▶│    Post-Processors      │ │
│  │  (E workers)│    │  (D workers)│    │    (P workers)          │ │
│  └─────────────┘    └─────────────┘    └─────────────────────────┘ │
│         │                  │                        │               │
│         ▼                  ▼                        ▼               │
│    [bounded chan]     [bounded chan]          [bounded chan]        │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    ▼               ▼               ▼
            ┌──────────┐    ┌──────────┐    ┌──────────┐
            │  Shared  │    │  Global  │    │ Progress │
            │Transport │    │  Limiter │    │   Hub    │
            └──────────┘    └──────────┘    └──────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Segment Downloader                            │
│                   internal/downloader/*.go                           │
│  ┌────────────┐   ┌────────────┐   ┌────────────┐   ┌────────────┐ │
│  │  Planner   │──▶│  Fetchers  │──▶│Ring Buffer │──▶│ Assembler  │ │
│  │  (DSS)     │   │ (goroutines)│   │ (bounded)  │   │ (pwrite)   │ │
│  └────────────┘   └────────────┘   └────────────┘   └────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```
