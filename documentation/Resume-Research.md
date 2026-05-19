# Resume & Resumability Research

> Research date: 2026-05-18  
> Implementation date: 2026-05-18  
> Branch: `resume` (created from `main`)  
> Status: **Implemented** — Tiers 1–3 are live. Tier 4 (job-level pipeline state) remains future work.  
> Sources: yt-dlp source code audit, ytgo codebase audit, external analysis (Claude Opus 4.7), web research.

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [yt-dlp Resumability — How It Works](#yt-dlp-resumability--how-it-works)
3. [ytgo Resumability — Current State](#ytgo-resumability--current-state)
4. [Flaws in yt-dlp's Resume (with ytgo comparisons)](#flaws-in-yt-dlps-resume-with-ytgo-comparisons)
5. [New Insights from External Analysis](#new-insights-from-external-analysis)
6. [Hidden ytgo Bug Neither Analysis Caught Initially](#hidden-ytgo-bug-neither-analysis-caught-initially)
7. [Implementation Plan (Tiered)](#implementation-plan-tiered)
8. [Scenario: External App Crash](#scenario-external-app-crash)

---

## Executive Summary

ytgo's **transport-layer** resume (segmented HTTP downloading with a `.segments` JSON sidecar) is already *more sophisticated* than yt-dlp's legacy single-stream byte-counting for direct HTTP files. However, yt-dlp wins on **integration** — its `.ytdl` fragment tracking, `.part` conventions, archive system, and retry logic are actually wired together end-to-end.

ytgo has the right primitives (bounded segment planning, `pwrite`, atomic sidecars) but lacks the **glue** that makes resume reliable and transparent across process restarts. The biggest gap is that ytgo's resume state is scoped to a **file path + URL**, not to a **video identity**. When YouTube presigned URLs expire (~6 hours), ytgo's resume state becomes useless.

This document proposes a tiered plan to close those gaps and build resumability that is meaningfully better than yt-dlp's.

---

## yt-dlp Resumability — How It Works

| Level | Mechanism | Details |
|---|---|---|
| **Single-file HTTP** | `.part` temp file + `Range` headers | Downloads to `filename.part`. Probes file size on restart. Resumes via `bytes=N-`. Chunk size is randomized (±5%) to avoid pattern detection. |
| **Fragmented (HLS/DASH)** | `.ytdl` JSON sidecar + per-fragment temp files | Each fragment downloads to `filename-FragN`, tracked in a `.ytdl` file storing `current_fragment.index`. Supports concurrent fragment downloads. |
| **Playlist/Batch** | Download archive (`--download-archive`) | Plain-text list of already-completed video IDs. Skips them on re-run. |
| **Retries** | `RetryManager` with configurable retries | HTTP errors, transport errors, and incomplete reads trigger automatic retry. Fragment-level retries are separate (`--fragment-retries`). |
| **Flags** | `-c`/`--continue` (default on), `--no-continue`, `--no-part`, `--force-overwrites` | Well-integrated; `--no-continue` forces restart. |

### Key yt-dlp Resume Flow (single-file HTTP)

1. Establish `resume_len` from the `.part` file size.
2. Send `Range: bytes=resume_len-` (or bounded chunk range).
3. Validate `Content-Range` response. If the server ignores `Range` and sends the full file, detect mismatch, report "unable to resume," wipe the file, and restart.
4. Handle `416 Range Not Satisfiable`: re-probe without `Range`. If reported length is within ±100 bytes of `resume_len`, consider the file already downloaded.
5. On transport errors mid-stream, close the stream, update `resume_len` from the `.part` file, and retry.

### Key yt-dlp Fragment Flow (HLS/DASH)

1. Each fragment gets its own temp file (`filename-FragN`).
2. A `.ytdl` JSON file tracks the next fragment index.
3. On restart, read `.ytdl`. If corrupt or inconsistent (`fragment_index > 0` but `resume_len == 0`), warn and restart from fragment 0.
4. Fragments are appended to the destination stream and deleted after successful write.

---

## ytgo Resumability — Current State

### What EXISTS

| Feature | File | Status |
|---|---|---|
| **Segment-level resume state** (`ResumeState` JSON sidecar) | `internal/downloader/resume.go` | ✅ Works. Stores `URL`, `DestPath`, `FileSize`, `Completed []ByteRange` in `{dest}.segments`. |
| **Segment-granular resume in `SegmentDownloader`** | `internal/downloader/segment.go` | ✅ Works. Loads `.segments`, computes `MissingRanges()`, only fetches missing segments. Saves on error, removes on success. |
| **Single-stream sequential resume** | `internal/downloader/downloader.go` | ✅ Works. `Download()` probes existing file size via `Stat()`, then issues bounded `Range` requests. |
| **Pre-allocation + pwrite** | `internal/downloader/resume.go` | ✅ Uses `Truncate` + `WriteAt` so segments can land at arbitrary offsets safely. No fragment temp files. |
| **Config flags** (`--no-continue`, `--no-overwrites`) | `cmd/root.go`, `config/config.go` | ⚠️ **Parsed but never read anywhere.** Dead code. |
| **Download archive** (completed video IDs) | `internal/archive/archive.go` | ✅ Works for skipping fully-completed videos. |

### What DOES NOT EXIST

| Gap | Impact |
|---|---|
| **`ContinuePartial` / `--no-continue` is unwired** | Passing `--no-continue` has **zero effect**. The downloader always loads `.segments` if present. No code deletes stale `.segments` or partial files on fresh start. |
| **No `.part` temp-file naming** | Downloads write **directly to the final filename** (e.g. `My Video [id].mp4`) plus a `.segments` sidecar. A crashed download looks like a complete file to the OS and to users. |
| **No resume state validation** | `LoadResumeState()` never checks if the stored `URL` or `FileSize` still matches the current extraction. YouTube presigned URLs expire; if re-extraction yields a new URL, ytgo blindly tries to resume with stale segment boundaries. |
| **No HTTP retry / transient failure recovery** | If a single segment fails (connection reset, timeout, 5xx), `eg.Wait()` aborts the **entire** download immediately. No retry, no exponential backoff. The `.segments` file is saved, but the user must manually re-run. |
| **No job-level (pipeline) state persistence** | `internal/core/engine.go` has `videoTask` — purely in-memory. If the app crashes **after** downloading video+audio but **before** merge/FFmpeg post-processing, the next run starts from scratch: re-extracts, re-selects formats, re-downloads. The `.segments` sidecars may help resume the HTTP layer, but everything above it is repeated. |
| **No playlist resume-at-position** | The archive skips fully-completed videos, but there is no cursor tracking "we were at playlist index 47." If a crash happens mid-playlist, the next run re-submits all prior entries to the worker pool (they get skipped by the archive, but extraction overhead is repeated). |
| **No periodic save of resume state** | `rs.Save()` is only called on error or at completion. If a segment completes and the process crashes before the next segment errors, that completion is lost even though the bytes are on disk. Safe, but wasteful. |

---

## Flaws in yt-dlp's Resume (with ytgo comparisons)

### 1. No integrity verification — silent corruption is possible

yt-dlp trusts the byte count on disk. There is no checksum of the existing `.part` file before issuing a `Range: bytes=N-` request. If those first N bytes were corrupted by a power loss, disk error, OOM kill mid-write, or a partial buffered write that never flushed, the resumed file will be silently broken.

> **ytgo comparison:** Our `pwrite` + segment-at-a-time model is *less* vulnerable than yt-dlp's sequential append model. A crash mid-segment only corrupts that one segment, and since we don't mark it complete, it gets overwritten on resume. However, disk/OOM/power loss could still corrupt a completed segment if the OS hasn't flushed writes. `fd.Sync()` at the end helps, but we don't sync per-segment.

### 2. URL expiry vs. resume time — the fundamental architectural flaw

YouTube's `googlevideo.com` URLs have an `expire=<unix_timestamp>` parameter. Typically ~6 hours. If you start a download, suspend your laptop overnight, and try to resume the next morning, yt-dlp will issue a `Range` request against an expired URL and get a 403. yt-dlp doesn't store the video ID alongside the `.part` file, so it can't re-extract. The user has to remember the original URL or start over.

> **ytgo comparison:** We have the exact same flaw. Our `ResumeState` stores the raw `URL` string. When it expires, the `.segments` sidecar is useless.

### 3. `--no-part` disables resume entirely

Documented as broken since 2021 and still unresolved. Users who want files written directly (common for media servers watching a directory) lose resumability as a side effect.

> **ytgo comparison:** We have the mirror bug — `--no-continue` is parsed but **unwired**. Same class of issue: a resume-control flag that does nothing.

### 4. Stale `.part` files leak after merge

Even when things mostly work, cleanup is broken. A subsequent run can't distinguish "this `.part` is from an in-progress download I should resume" from "this `.part` is orphaned garbage from a previous run."

> **ytgo comparison:** Not applicable yet because we don't use `.part` files. However, if we introduce `.part` naming (recommended), we must implement atomic rename + cleanup to avoid creating this bug.

### 5. Resume can stall indefinitely on certain videos

For some YouTube videos, subsequent invocations cannot resume. The download hangs with "Got no data blocks" and never recovers. There's no automatic fallback to "discard the `.part` and restart fresh."

> **ytgo comparison:** We don't have auto-fallback either. If resume state is stale and the server misbehaves, we should detect this and restart rather than loop forever.

### 6. Livestream resume renumbers fragments incorrectly

For long livestreams, the code renumbers fragments instead of using YouTube's static `sq/N` numbering, causing resumed downloads to skip days of content silently.

> **ytgo comparison:** Not directly applicable — we don't support livestreams yet. But if we add HLS/DASH support later, this is a cautionary tale: use the server's native fragment identifiers, not a local counter.

### 7. Fragmented downloads have an "all or nothing" success bar

When fragments fail mid-download, the existing fragments stay on disk but the metadata about which ones are complete is fragile. If the program crashes between "fragment downloaded" and "fragment recorded as downloaded," the finished file can be added to the archive despite being incomplete.

> **ytgo comparison:** We are better here because our segments are bounded byte ranges, not arbitrary HLS fragments. A crash only loses un-saved completions. But see "No periodic save" above — we still lose progress between saves.

### 8. Resume re-triggers the throttling problem

yt-dlp's single-file resume issues an **unbounded** `Range: bytes=N-`. That's exactly the request pattern that drops to ~32 KB/s. So a video that finished in 5s originally might resume at dial-up speeds. yt-dlp's chunked downloader path avoids this, but the legacy single-stream path does not.

> **ytgo comparison:** ✅ **Already fixed.** Both `SegmentDownloader` and `Downloader.Download()` use bounded `bytes=start-end` chunks. Resume naturally stays within ~10 MB chunks.

---

## New Insights from External Analysis

Three genuinely new insights were identified that the initial ytgo audit missed:

### 🔴 Insight #1: Store Video ID, Not URL

This is the single biggest architectural win. YouTube URLs expire. Storing `VideoID` and `FormatID` in the sidecar allows the engine to **re-extract a fresh URL on resume**. yt-dlp cannot easily do this because its downloader layer is decoupled from the extractor layer. ytgo's `Engine` owns both, so we can.

### 🔴 Insight #2: `clen=` Query Parameter as Free Integrity Signal

YouTube direct URLs include `clen=<content_length>` in the query string. We can:
1. Parse `clen` from the URL when starting a download.
2. Store it in the resume state.
3. On completion, verify the actual file size matches `clen`.
4. On resume, if the new URL's `clen` differs from the stored one, discard the resume state.

This fixes silent corruption without needing cryptographic hashes.

### 🔴 Insight #3: Periodic Save of Resume State

Our `segment.go` only calls `rs.Save()` on error or at completion:

```go
for _, seg := range missing {
    if err := sd.fetchSegment(ctx, url, seg, fd, &downloaded); err != nil {
        _ = rs.Save()
        return fmt.Errorf("segment download failed: %w", err)
    }
    rs.Completed = append(rs.Completed, seg)
    // ❌ NO rs.Save() here!
}
```

If the process is killed after segment 5 completes but before segment 6 starts, segments 1-5 are written to disk via `pwrite` but **not recorded in `.segments`**. On restart, we re-download them. Safe, but wasteful. We should call `rs.Save()` after every segment completion (debounced).

---

## Hidden ytgo Bug Neither Analysis Caught Initially

When re-reading `segment.go` with the external insights in mind, a scoping bug emerges:

```go
// Load or create resume state
rs, err := LoadResumeState(destPath)
if rs == nil {
    rs = &ResumeState{URL: url, DestPath: destPath, FileSize: totalSize}
}
```

The resume state is keyed **only by `DestPath`**. If a user changes `--format` between runs:

- **Old run:** format `137` (1080p video, 500 MB). Leaves `My Video [id].mp4.segments`.
- **New run:** format `251` (audio only, 5 MB). Same output path (or same stem).
- **Result:** We load the old 500 MB state, pre-allocate a 500 MB file, and download 5 MB of audio into it. The file "works" but is 500 MB full of zeros.

**Fix:** Scope the resume state by `(VideoID, FormatID)` or at minimum validate that current download parameters match the stored state before trusting it.

---

## Implementation Plan (Tiered)

### Tier 1: Fix the Broken Stuff (Safety) ✅ Implemented

| # | Task | Files | Effort |
|---|---|---|---|
| 1.1 | **Wire up `--no-continue`**. Actually read `cfg.ContinuePartial` in `segment.go` and `downloader.go`. When disabled, delete existing `.segments` and any partial file before starting. | `cmd/root.go`, `internal/downloader/segment.go` | Small |
| 1.2 | **Add URL + size validation** on `.segments` load. Compare stored `URL` and `FileSize` against the current download. If different, discard the state and start fresh. | `internal/downloader/resume.go`, `internal/downloader/segment.go` | Small |
| 1.3 | **Periodic save**. Call `rs.Save()` after every segment completion. Use a cheap debounce (e.g., `sync.Once` per second or a simple "save if last save > 1s ago") to avoid I/O thrashing. | `internal/downloader/segment.go` | Small |
| 1.4 | **Add `VideoID` and `FormatID` to `ResumeState`**. Scope resume state by identity, not just path. Reject `.segments` files whose `VideoID`/`FormatID` don't match. | `internal/downloader/resume.go`, `internal/core/engine.go` | Small |

### Tier 2: The "Video ID" Pivot (The Big Architectural Win) ✅ Implemented

| # | Task | Files | Effort |
|---|---|---|---|
| 2.1 | **Store video ID, not URL, as the primary resume key.** The sidecar should store `VideoID` + `FormatID` + `clen`. The actual URL is ephemeral. | `internal/downloader/resume.go` | Small |
| 2.2 | **On 403 / URL expiry, re-extract.** If the server rejects the stored URL with 403, trigger a fresh extraction to get a new signed URL, then continue from the next missing segment. This is the feature yt-dlp literally cannot do. | `internal/core/engine.go`, `internal/downloader/segment.go` | Medium |
| 2.3 | **Parse `clen=` from URL.** Store expected size. Verify on completion. On resume, if the new URL's `clen` differs, discard state. | `internal/downloader/downloader.go`, `internal/downloader/resume.go` | Small |

### Tier 3: Integrity & Cleanup ✅ Implemented

| # | Task | Files | Effort |
|---|---|---|---|
| 3.1 | **Introduce `.part` naming.** Download to `destPath + ".part"`, rename on success. Prevents incomplete files from looking final to users and to media servers. | `internal/downloader/segment.go`, `internal/core/engine.go` | Small |
| 3.2 | **Atomic cleanup.** On successful completion: `os.Rename(part, final)` + delete `.segments` as an atomic transaction. If rename succeeds but segment deletion fails, the file is still valid — the sidecar is harmless. | `internal/downloader/segment.go` | Small |
| 3.3 | **Per-segment retry.** If `fetchSegment` fails, retry 2-3 times with exponential backoff before aborting the whole job. | `internal/downloader/segment.go` | Small |

### Tier 4: Job-Level Resume (External App Scenario)

| # | Task | Files | Effort |
|---|---|---|---|
| 4.1 | **`.ytgo` pipeline state file.** Persist `videoTask` stage (extracted → downloaded → merged → post-processed). On restart, skip already-completed stages. | `internal/core/engine.go` | Medium |
| 4.2 | **Playlist cursor.** Write a `playlist.ytgo` file with per-entry status (`pending` / `downloading` / `completed`). Resume at the first non-completed index instead of re-submitting all entries. | `internal/core/engine.go` | Medium |
| 4.3 | **Library-friendly API.** Expose `ResumeManager` or `JobState` in `pkg/ytgo/` so external apps can persist state to their own database rather than filesystem sidecars. | `pkg/ytgo/` | Medium |

---

## Scenario: External App Crash

> *"I have another app that uses ytgo to download multiple videos. I accidentally exit. When I open the app again, how does ytgo resume?"*

### Behavior After Implementation (Tiers 1–3)

1. The external app calls `engine.Run()` again with the same playlist URL.
2. ytgo re-extracts the entire playlist.
3. Videos already in the archive → skipped. ✅
4. Video that was **mid-download** → `.part` + `.part.segments` exist. HTTP download resumes from the last completed segment. If the URL expired, ytgo re-extracts and continues. ✅
5. Video that had **both formats downloaded but merge crashed** → `.part` files are gone (download stage succeeded), so ytgo **starts over entirely**: re-extracts, re-downloads both formats, then merges. ❌ *(Tier 4 would fix this.)*
6. Video that was **mid-post-process** (e.g. FFmpeg embed running) → same as above, total restart. ❌ *(Tier 4 would fix this.)*

### Current State

After Tier 1-3:
- HTTP downloads survive URL expiry (re-extraction on 403).
- Incomplete files are `.part` files, never confused with final files.
- Segment progress is saved periodically, so crashes lose at most one segment's worth of work.
- `--no-continue` and `--no-overwrites` are fully wired and working.

Tier 4 (job-level pipeline state) remains future work:
- The `.ytgo` job state would track pipeline stage. If merge crashed, the next run skips extraction and download, goes straight to merge.
- The `playlist.ytgo` cursor would mean the external app doesn't re-extract already-processed entries.
- The external app could read `.ytgo` files to show UI progress ("3 of 10 videos done, #4 is merging").

---

## Related Files

| File | Role |
|---|---|
| `internal/downloader/resume.go` | `ResumeState` struct, save/load/remove |
| `internal/downloader/segment.go` | `SegmentDownloader`, segment loop, `rs.Save()` calls |
| `internal/downloader/downloader.go` | `Downloader.DownloadToFile`, fallback sequential path |
| `internal/downloader/planner.go` | `PlanSegments`, `ByteRange` |
| `internal/core/engine.go` | `Engine.Run`, `videoTask`, playlist orchestration |
| `internal/config/config.go` | `ContinuePartial`, `NoOverwrites` flags |
| `cmd/root.go` | Flag binding, manual unmarshal into config |
| `internal/archive/archive.go` | Download archive (completed video IDs) |

---

*End of document.*
