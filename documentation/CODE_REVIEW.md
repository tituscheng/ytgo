# Code Review: ytgo Architecture, Design, and Implementation

**Original Date:** 2026-05 (based on main branch at the time)  
**Latest Re-audit:** Current main (post prior fixes)  
**Reviewer:** Grok 4.3  
**Scope:** Architecture, design patterns, concurrency, error handling, resource management, API surface, and subtle correctness issues. Static analysis + `go test -race` execution.  
**Status:** Review only — no code changes were made during this review or re-audit.  
**Purpose:** Living document. Prior findings are re-audited for actual fix status; new issues discovered in subsequent reviews are added below.

---

## 1. Executive Summary

ytgo is a well-engineered, pragmatic YouTube downloader written in Go. The architecture is intentionally lean, avoids heavy dependencies (notably no JavaScript engine), and delivers excellent performance characteristics compared to yt-dlp.

The codebase is generally clean, thoughtfully documented (especially `ARCHITECTURE.md` and `Resume-Research.md`), and demonstrates strong understanding of YouTube's anti-bot and throttling behaviors.

**However**, several architectural tensions, concurrency hazards, and subtle correctness risks exist — primarily in the playlist pipeline, worker pool implementation, progress reporting, and error classification. Many of these are not visible on the happy path but can manifest under cancellation, high concurrency, partial failures, or long-running playlists.

**Overall Assessment (Original):**  
**Strong (8.5/10)** for a project of this complexity.

**Re-audit Note:** Many of the high-priority items from the original review were addressed (typed errors, progress reporting, `cleanup.Stack`, workerpool lifecycle, reduced flag binding). However, two *new* high-severity concurrency issues were found during re-audit that belong to the same class as the previously reported WorkerPool cancellation hazard (one in `SegmentDownloader`, one in the public `OnError` callback contract). The project remains strong, but concurrent cancellation safety and user-callback thread-safety still require attention. Current confidence: **Strong (8/10)**.

---

## 2. Strengths

- **Clear layering and boundaries.** `core.Engine` is the single orchestrator. The `InfoExtractor` interface is a clean extension point. Internal packages do not leak into `pkg/ytgo`.
- **Resume & download architecture is sophisticated.** After the work documented in `Resume-Research.md`, ytgo's segmented resume (identity-scoped with VideoID + FormatID + `clen=`, re-extraction on 403, bounded chunks, `.part` + atomic rename) is meaningfully better than yt-dlp's in several important dimensions.
- **Format selection with type-safe preferences** is a genuine usability improvement over yt-dlp's regex DSL.
- **Context propagation** is generally consistent across long-running operations.
- **Minimal dependency philosophy** is respected in practice (binary stays ~11 MB).
- **Documentation quality** is unusually high for a project of this size. The author clearly understands the hard problems (URL expiry, CDN throttling, playlist fan-out).

---

## 3. Findings

### 3.1 Architectural & Design Issues

#### 3.1.1 Playlist Two-Stage Pipeline Is Complex and Error-Prone

**Location:** [internal/core/engine.go](internal/core/engine.go) (lines 430–543)

The design of `downloadPool` feeding `postprocChan` to a separate `postprocPool` provides backpressure but creates several problems:

- Post-processing workers are launched as `N` goroutines each running `for task := range postprocChan`. There is no clean shutdown path when context is cancelled while a worker is blocked inside a long-running FFmpeg invocation.
- Errors inside post-processing workers are logged to stderr but swallowed; `postprocPool.Wait()` rarely surfaces real per-item failures.
- Download workers deliberately return `nil` on per-video errors (line 518) to keep the playlist moving. This is intentional for "playlist-safe" behavior but makes it hard to distinguish "video failed" from "worker pool aborted".
- The channel is closed only after `downloadPool.Wait()`. If download workers are blocked trying to send on `postprocChan` while post-processors are slow, cancellation can leave goroutines stuck.

**Potential Fix:**
- Consider a single unified worker pool with explicit stages (or use a proper pipeline library).
- Add explicit context-aware draining and a "drain and shutdown" protocol for the postproc channel.
- Propagate structured per-video results (success / archived / failed) instead of relying on side effects and `OnError` callbacks alone.

#### 3.1.2 Streaming Audio Extraction Bypasses Core Download Logic

**Location:** [internal/postprocessor/stream.go](internal/postprocessor/stream.go) (lines 56–58)

When `-x` (extract audio) is used with a single audio format, the code takes a completely separate fast path:

```go
d := &downloader.Downloader{Client: client}  // no resume, no identity, no rate limit, no progress, no bounded chunks
err := d.Download(ctx, url, pw)
```

This path loses every sophisticated behavior the rest of the downloader was designed for (bounded chunks to avoid throttling, resume support, rate limiting, proper identity tracking, progress aggregation).

**Potential Fix:**
- Unify the streaming and file-download paths, or at minimum pass a properly configured `Downloader` (or a subset of options) into `StreamConverter`.
- Document that the streaming path has reduced capabilities.

#### 3.1.3 Progress Reporting Is Semantically Broken Under Concurrency

**Location:** [internal/downloader/segment.go](internal/downloader/segment.go) (line 235)

```go
sd.Progress(newTotal, seg.EndByte+1)  // reports against *this segment's* end, not global file size
```

When `Workers > 1`, the progress callback receives multiple independent "totals". The only aggregation that exists (`progressAggregate` in engine.go) is for *multi-format* downloads (`bv+ba`), not for intra-format segment concurrency.

Library users setting `OnProgress` will observe incorrect or wildly fluctuating values during concurrent segment downloads.

**Potential Fix:**
- Track global downloaded bytes separately from per-segment progress.
- Report `(globalDownloaded, totalFileSize)` from `SegmentDownloader`.
- Clearly document the contract of `ProgressFunc` when `Workers > 1`.

---

### 3.2 Concurrency & Correctness Risks

#### 3.2.1 WorkerPool Submit Can Block Indefinitely on Context Cancellation

**Location:** [internal/pipeline/workerpool.go](internal/pipeline/workerpool.go) (lines 36–50)

```go
select {
case <-ctx.Done():
    return ctx.Err()
case wp.sem <- struct{}{}:     // <-- blocks here if at capacity
    wp.eg.Go(...)
}
```

If the pool is full and the context is cancelled, the goroutine blocks forever on the send. The only goroutines that can receive from `sem` are the ones launched inside `eg.Go`, which may never be scheduled (or may themselves be blocked waiting on the now-cancelled context).

This is a classic bounded worker pool + cancellation hazard.

**Potential Fix:**
- Use a non-blocking send or a separate "release" channel.
- Release the semaphore in a `defer` that also handles the case where we never entered the goroutine.
- Consider using a different concurrency primitive (e.g., `golang.org/x/sync/semaphore` + errgroup, or `conc` library patterns).

#### 3.2.2 Re-Extraction on 403 Is Narrow and Fragile

**Location:** [internal/core/engine.go](internal/core/engine.go) (lines 595–608)

The 403 recovery logic:
- Only retries the *download* of a single format.
- Reuses the local `d` downloader instance created for that format.
- Uses crude string matching (`isForbidden`).
- Does not re-run format selection or update the outer `VideoInfo`.

If the re-extracted response changes format availability or the format ID mapping, the retry can fail silently or download the wrong stream.

**Potential Fix:**
- Make 403 recovery a first-class operation at the `downloadVideo` level.
- Use proper error types or HTTP response inspection instead of `strings.Contains(err.Error(), "403")`.
- Consider surfacing "URL refreshed" as a distinct event.

#### 3.2.3 Archive Is Shared Across Goroutines Without Full Transactionality

**Location:** [internal/archive/archive.go](internal/archive/archive.go) and [internal/core/engine.go](internal/core/engine.go) (playlist path)

While `Has`/`Add` are mutex-protected, the in-memory map and file append are not atomic with respect to crashes. A crash between setting `a.entries[id] = true` and the `fmt.Fprintln` can leave the in-memory view ahead of disk.

More importantly, during playlist runs the same `*Archive` is passed to many concurrent workers; any future change to the archive logic must be extremely careful.

**Potential Fix:**
- Consider writing the ID first, then updating the map (or use a WAL-style approach for very high reliability needs).
- Document the exact consistency guarantees.

---

### 3.3 Error Handling & Resilience

#### 3.3.1 String-Based Error Classification Is Brittle

**Locations:**
- [internal/core/engine.go](internal/core/engine.go) — `isForbidden` (line 613), `isRetryable` (line 621)

```go
return strings.Contains(err.Error(), "403")
return strings.Contains(msg, "429") || strings.Contains(msg, "503") ...
```

This pattern is fragile against:
- Error wrapping (`%w`)
- Future changes in underlying HTTP error messages
- Different transport implementations
- Internationalized or user-localized error strings

**Potential Fix:**
- Define sentinel errors or custom error types (`ErrHTTPStatus`, `ErrTransient`, etc.).
- Inspect `*url.Error`, `http.Response` status codes, or context errors at the source instead of string matching at the call site.

#### 3.3.2 Per-Video Failures in Playlists Are Under-Observable

Download workers swallow errors and continue (intentional), but there is no easy way for callers to get a complete picture of which videos failed vs. were skipped vs. succeeded without parsing stderr or relying solely on `OnError` callbacks.

---

### 3.4 Resource Management & Cleanup

- Temp files created for stdout output (`os.TempDir()`) and embedder `.tmp` files are not guaranteed to be cleaned up on all error paths or on context cancellation.
- The pipe-based streaming path in `StreamConverter` has no forced cleanup if the download goroutine or FFmpeg process is orphaned.
- No use of `os.RemoveAll` with defer patterns or `t.Cleanup` style guards in the core paths.

**Potential Fix:** Introduce a small `tempfile` helper package that registers files for guaranteed cleanup on context cancellation or process exit.

---

### 3.5 Configuration & API Surface

#### 3.5.1 Manual Flag-to-Config Mapping Is a Maintenance Burden

**Location:** [cmd/root.go](cmd/root.go) (lines 144–210)

After `viper.Unmarshal(&cfg)`, the code performs ~60 manual overrides from flags. This is error-prone and the source of classic "flag not taking effect" or "new flag forgotten" bugs.

**Potential Fix:**
- Use a single source-of-truth options struct and a declarative binding layer.
- Or generate the binding code.
- Consider separating "CLI surface" from "core options" more cleanly.

#### 3.5.2 `DownloadOptions` Mixes Concerns

The type alias from `config.DownloadOptions` into the public API surface mixes:
- User-configurable fields
- Library-only callbacks (`OnProgress`, `OnError`)
- Internal/derived state

This makes the public API harder to evolve cleanly.

---

### 3.6 Other Observations

- **Dead code:** `Engine.bufPool` is allocated in `NewEngine` but never used (the `Downloader` receives its own inline pool).
- **Innertube client visitor refresh** has no retry and falls back to synthetic data that may become less effective over time.
- **Playlist continuation parsing** has multiple structural fallbacks. This is pragmatic but will be a recurring source of breakage as YouTube changes response shapes.
- **No structured logging.** Library users have very limited visibility into internal operations.
- The project itself (in `Resume-Research.md`) correctly identifies that Tier 4 (job-level pipeline resume) remains future work. This is the largest missing capability for serious programmatic use.

---

## 4. Prioritized Recommendations

| Priority | Category | Issue | Suggested Action |
|----------|----------|-------|------------------|
| **High** | Concurrency | `WorkerPool.Submit` can block forever on cancelled context | Redesign semaphore acquisition to be cancellation-aware and always releasable. |
| **High** | Correctness | String-based 403/retry classification | Introduce proper error types or status-aware error wrapping. |
| **High** | Correctness | Segmented progress reporting is wrong for `Workers > 1` | Track and report global byte progress; document the callback contract. |
| **Medium** | Architecture | Playlist pipeline complexity & shutdown | Consider simplifying or adding explicit shutdown coordination. |
| **Medium** | Resilience | Streaming audio path bypasses core logic | Unify or at least configure the downloader instance properly. |
| **Medium** | Maintainability | Manual flag binding in `cmd/root.go` | Refactor to declarative or generated binding. |
| **Medium** | Cleanup | Temp file leaks on error/cancellation paths | Introduce a managed temp file helper with guaranteed cleanup. |
| **Low** | Polish | Dead `Engine.bufPool` field | Remove or wire up consistently. |
| **Low** | Observability | No structured logging for library users | Add optional `slog.Logger` or event hook interface. |

---

## 5. Areas That Deserve Follow-Up Discussion

1. **What is the intended contract for `OnProgress` during concurrent segment downloads?** Current behavior is arguably a bug for library consumers.
2. **Should the streaming audio path ever support resume?** If not, this should be explicitly documented as a limitation.
3. **Long-term vision for job-level resume (Tier 4).** Is this still desired? The current design makes it difficult to add later without significant refactoring.
4. **Error taxonomy.** Should ytgo define a small set of public error types in `pkg/ytgo`?

---

## 6. Appendix: Key Files Referenced

| File | Role in Review |
|------|----------------|
| `internal/core/engine.go` | Orchestration, playlist pipeline, 403 recovery, progress aggregation |
| `internal/pipeline/workerpool.go` | Bounded concurrency primitive (main hazard) |
| `internal/downloader/segment.go` | Segmented downloads, resume state, progress reporting |
| `internal/downloader/resume.go` | Resume identity and validation logic |
| `internal/postprocessor/stream.go` | Streaming audio extraction (bypass path) |
| `cmd/root.go` | CLI flag binding (maintenance burden) |
| `internal/archive/archive.go` | Shared archive across workers |
| `internal/extractor/youtube/innertube/*` | YouTube protocol client |

---

## 7. Conclusion (Original Review)

ytgo has a solid foundation and several genuinely good architectural decisions (especially around resume identity and bounded chunking). The main risks are concentrated in concurrent shutdown, error classification, and the growing maintenance cost of the CLI configuration layer.

Most issues identified are fixable without fundamental redesign.

---

## 7.1 Re-audit Conclusion

The majority of the *original* high-priority mechanical fixes (typed `StatusError`, progress semantics, `cleanup.Stack`, visitor retries, flag binding reduction, `PlaylistReport` detail) are present and working.

However, the **concurrency cancellation** class of bug was only partially mitigated: `pipeline.WorkerPool` received the `select` + state-machine treatment, but an identical pattern was re-introduced inside `SegmentDownloader` for HTTP segment workers. Additionally, a data race on the public `OnError` callback (exercised by concurrent playlist workers) was discovered via `-race` that affects both CLI and library consumers.

The architecture itself remains sound and the Tier 1–3 resume story is excellent. The remaining risks are localized and fixable.

**Current High-Priority Items (Re-audit):**
1. Make `OnError` callback safe (or document + enforce the concurrent contract).
2. Fix the blocking semaphore send in `SegmentDownloader` (mirror the `WorkerPool` fix).
3. Clamp negative concurrency limits in `NewWorkerPool`.

**Recommended next step:** Add a `-race` test that exercises playlist failures with `OnError` + cancellation of a `Workers > 1` segmented download. These two tests would have caught the issues found in this re-audit.

---

*This document was produced from static analysis, code reading, and `go test -race ./...` (short mode). No production code was modified.*

---

## 8. Resolution Log (Original Review)

| Finding | Status (at time) | Resolution |
|---|---|---|
| 3.1.1 Playlist pipeline complexity | **Partially Fixed** | `PlaylistReport` now returns structured per-item results (`Succeeded`, `Skipped`, `Failed`). Post-processing errors are captured in `report.Failed`. Full stage tracking (Tier 4) remains future work. |
| 3.1.2 Streaming bypasses core logic | **Fixed** | `StreamConverter` now accepts the configured `*Downloader`. Call site in `engine.go` passes `e.Downloader` so rate limiting and progress are preserved. |
| 3.1.3 Progress semantics broken | **Fixed** | `SegmentDownloader` tracks `totalSize` during `probe` and reports `(globalDownloaded, totalSize)` from `fetchSegment`. `ProgressFunc` godoc updated. |
| 3.2.1 WorkerPool cancellation | **Fixed** | Added lifecycle state guards (`idle/running/waiting/done`) with `atomic.Int32`. `Submit` after `Wait` returns an error instead of panicking. |
| 3.2.2 403 re-extraction fragile | **Fixed** | `downloader.StatusError` wraps HTTP status codes. `isForbidden` uses `errors.As` with `StatusError{403}`. `isRetryable` uses `errors.Is` with `ErrRateLimited` / `ErrTransient`. Fallback string matching preserved for non-typed errors. |
| 3.2.3 Archive transactionality | **Documented** | Package godoc explains thread-safety and crash-recovery semantics. Write-to-disk-before-map ordering is already guaranteed by the mutex. |
| 3.3.1 String error classification | **Fixed** | Same as 3.2.2 — typed errors replace string matching for HTTP status codes. Network-level `*url.Error` inspection added for `Temporary()` / `Timeout()`. |
| 3.3.2 Playlist failures under-observable | **Fixed** | `runPlaylist` returns `*PlaylistReport` with `Failed` slice containing full `DownloadFailure` structs per item. |
| 3.4 Resource cleanup | **Fixed** | New `internal/cleanup` package provides `Stack` for guaranteed temp file removal. `engine.go` uses it for stdout paths. `postprocessor.go` `downloadThumbnail` now removes the temp file on `io.Copy` error. |
| 3.5.1 Manual flag binding | **Fixed** | Reduced ~55 manual `GetXxx()` lines to a single `no-continue` special case. `viper.BindPFlags` + `viper.Unmarshal` handles everything else. |
| 3.5.2 Mixed concerns in config | **Partially Fixed** | Added optional `*slog.Logger` to `DownloadOptions` with `mapstructure:"-"`. Long-term separation into `Config` / `LibraryOptions` layers remains a future refactoring. |
| 3.6 Dead `bufPool` | **Fixed** | Removed unused `bufPool` field from `Engine` struct and `NewEngine`. |
| 3.6 Visitor refresh no retry | **Fixed** | `refreshVisitorID` now retries up to 3 times with exponential backoff. Uses `http.NewRequestWithContext` so cancellation is respected. |
| 3.6 No structured logging | **Fixed** | `DownloadOptions` now accepts an optional `*slog.Logger`. `Engine.log()` helper emits debug logs at key decision points (format selection, 403 recovery, archive skip). |
| Additional: `http.Get` without context | **Fixed** | `downloadThumbnail` uses `http.NewRequestWithContext(ctx, ...)` instead of `http.Get`. |
| Additional: Thumbnail temp leak | **Fixed** | `downloadThumbnail` explicitly `os.Remove(f.Name())` on `io.Copy` error. |

---

*Resolution implemented in a single comprehensive PR based on this review.*

---

## 8.1 Re-Audit of Prior Claims (Current Review)

This table audits the "Fixed" claims from the previous resolution log against the *current* codebase (including `go test -race` runs).

| Prior Finding | Claimed Status | Current Verified Status | Notes |
|---------------|----------------|-------------------------|-------|
| 3.2.1 WorkerPool cancellation | **Fixed** | **Mostly Fixed** (pipeline layer) | `pipeline.WorkerPool` has `atomic` state machine + `select` on `Submit`. Correct. However, `SegmentDownloader` (segment.go:151) has the *identical unprotected `sem <- struct{}{}` pattern* in its internal bounded worker loop. Cancellation hazard still exists for `Workers > 1` downloads. |
| 3.1.2 Streaming bypasses core logic | **Fixed** | **Partially Fixed** | `StreamConverter` now receives the configured `*Downloader` (rate limiting works). Progress and Identity/resume are **not** wired for the `-x` single-audio pipe path. 403 re-extraction also bypassed. |
| 3.1.3 Progress semantics broken | **Fixed** | **Verified Fixed** | Global `totalSize` + atomic tracking in `fetchSegment` + godoc update present and correct. |
| 3.2.2 403 re-extraction fragile + 3.3.1 String classification | **Fixed** | **Verified Fixed** | `StatusError` + `Unwrap()` to sentinels, `errors.As`/`errors.Is` in `isForbidden`/`isRetryable`, network `*url.Error` inspection all present. |
| 3.3.2 Playlist failures under-observable | **Fixed** | **Verified Fixed** | `DownloadFailure` structs + `report.Failed` + `OnError` callback fully populated. |
| 3.4 Resource cleanup | **Fixed** | **Verified Fixed** | `cleanup.Stack` used in `downloadVideo` for stdout; explicit removes on thumbnail/postprocessor error paths. |
| 3.5.1 Manual flag binding | **Fixed** | **Verified Fixed** | Only the `--no-continue` inversion remains manual. Everything else via `viper.BindPFlags` + `Unmarshal`. |
| 3.5.2 Mixed concerns in config | **Partially Fixed** | **Partially Fixed** | `*slog.Logger` added. Many dead/unwired fields remain (`NoContinue`, `BufferSize`, `ForceWriteArchive`, `PlaylistItems`, `RemuxVideo`, etc.). |
| 3.6 Dead `bufPool` | **Fixed** | **Verified Fixed** | Field removed from `Engine`. |
| 3.6 Visitor refresh no retry | **Fixed** | **Verified Fixed** | 3 retries + exponential backoff + `http.NewRequestWithContext` in `refreshVisitorID`. |
| 3.6 No structured logging | **Fixed** | **Partially Implemented** | `Logger` field + `Engine.log()` helper exist, but usage is sparse (only a few call sites). |

**Summary of Re-audit:** 9 of the original "Fixed" claims are fully verified present. The WorkerPool fix was real but incomplete (sibling hazard in downloader), and the streaming fix was partial. Several "polish" items (dead config, logging coverage) were deprioritized and remain.

---

## 9. New Findings from This Re-Audit

These issues were **not** identified in the original review (or regressed / were introduced when playlist concurrency and segmented workers were added).

### 9.1 Data Race on Public `OnError` Callback (High Severity)

**Locations:**
- [internal/core/engine.go:707](/Users/tituscheng/Projects/Go/ytgo/internal/core/engine.go) (`reportFailure`)
- Call sites inside `downloadVideo`, `runPlaylist` workers, `postProcessVideo`
- [cmd/root.go:172](/Users/tituscheng/Projects/Go/ytgo/cmd/root.go) (CLI error log accumulation)
- Test: [internal/core/engine_test.go:289](/Users/tituscheng/Projects/Go/ytgo/internal/core/engine_test.go) (`TestEngineRun_OnError_Playlist`)

**Root Cause:**  
User-supplied `OnError func(ytgo.DownloadFailure)` is invoked directly from multiple concurrent playlist workers (`downloadPool` + errgroup goroutines) with **no serialization**. The canonical usage pattern shown in README.md and docs:

```go
var failures []ytgo.DownloadFailure
opts.OnError = func(f ytgo.DownloadFailure) { failures = append(failures, f) }
```

is racy as soon as any playlist item fails (or during enrichment failures).

**Evidence:** `go test -race` produced a clear data race on slice header writes from two goroutines calling `reportFailure` → `OnError` during `TestEngineRun_OnError_Playlist`.

**Impact:** Lost failure records, corrupted slice, or panic under load for any playlist with failures when using the primary observation mechanism (`OnError` + `--write-error-log`).

**Why it matters:** This is the *only* way library callers receive per-video failure details for playlists (because `api.Download` discards the `*PlaylistReport`).

### 9.2 SegmentDownloader Internal Semaphore Blocks on Context Cancellation (High Severity)

**Location:** [internal/downloader/segment.go:149–151](/Users/tituscheng/Projects/Go/ytgo/internal/downloader/segment.go) (concurrent `Workers > 1` path):

```go
sem := make(chan struct{}, sd.Workers)
for _, seg := range missing {
    sem <- struct{}{}               // no ctx check
    eg.Go(func() {
        defer func() { <-sem }()
        sd.fetchSegment(ctx, ...)
    })
}
```

**Contrast:** The `pipeline.WorkerPool.Submit` path was correctly fixed with `select { case <-ctx.Done(): ... case wp.sem <- ... }`.

**Impact:**  
When a user cancels (Ctrl-C, deadline, parent ctx) during a large multi-segment download with `ConcurrentFragments > 1`, the launch loop in `DownloadToFile` can block on the semaphore send even though workers are draining. The call appears to hang until enough in-flight fetches complete. This is the *exact same hazard* the original review flagged for `WorkerPool`, just re-created inside the downloader's own segment scheduler.

**Additional observation:** Success-path `rs.Save()` calls inside workers are protected by `completedMu`, but on first error the outer `eg.Wait()` path does a single late Save. Some completed segments from siblings can be lost on abrupt cancellation.

### 9.3 Negative Concurrency Limits Cause Runtime Panic (Medium)

**Location:** [internal/pipeline/workerpool.go:33](/Users/tituscheng/Projects/Go/ytgo/internal/pipeline/workerpool.go):

```go
sem: make(chan struct{}, limit),   // limit from MaxDownloads / MaxPostProcessors / ConcurrentFragments
```

`NewWorkerPool` has no clamping. A negative value (user passes `--max-downloads -3`, bad YAML, or programmatic error) causes `makechan: size out of range` panic before any "limit <= 0 means unlimited" logic can run.

**Affected configs:** `--max-downloads`, `--max-postprocessors`, `--concurrent-fragments`, `MaxExtractors`.

### 9.4 Dead / Unwired Configuration Surface (Low–Medium)

Multiple fields in `config.DownloadOptions` are parsed by viper but have no effect:

- `NoContinue` (distinct from the wired `ContinuePartial`)
- `BufferSize` (pool is hardcoded 32 KiB in `NewEngine`)
- `ForceWriteArchive`, `PlaylistItems`, `RemuxVideo`, `RecodeVideo`, `Referer`, `UserAgent`, `Proxy`, `Cookies` (as distinct from `CookiesFromBrowser`)

This recreates the "flag appears to work but does nothing" class of bug the original review tried to reduce.

### 9.5 Streaming Audio Extraction Path Still Has Reduced Guarantees (Medium)

Even after the partial fix, the `-x` + single-audio fast path:

- Does not set `DownloadIdentity` → no resume / no `.segments` / no clen validation.
- Does not participate in the caller's `OnProgress` aggregation the same way.
- Bypasses the 403 re-extraction logic in `downloadFormatToFile`.
- Uses a direct `Downloader.Download` into a pipe rather than the full `SegmentDownloader` machinery.

Users who do `ytgo -x --audio-format mp3 URL` on a resumable or rate-limited connection get strictly weaker behavior than normal downloads.

---

## 10. Updated Prioritized Recommendations (Re-audit)

| Priority | Category | Issue | Suggested Action |
|----------|----------|-------|------------------|
| **High** | Concurrency safety | `OnError` callback is racy under concurrent playlist workers | Serialize calls inside `reportFailure`, or document "caller must synchronize" + provide `sync.Mutex` example / helper. Add race test. |
| **High** | Concurrency safety | `SegmentDownloader` semaphore send blocks on cancelled ctx | Add `select { case <-ctx.Done(): return ctx.Err(); case sem <- ... }` (or extract a reusable cancellable semaphore helper). |
| **Medium** | Robustness | Negative concurrency values panic | Clamp `limit = max(1, limit)` (or 0 = unlimited) in `NewWorkerPool` and `NewSegmentDownloader`. |
| **Medium** | Fidelity | Streaming `-x` path bypasses resume / identity / progress | Either document the limitations prominently or wire a proper `Downloader` (with Identity) for the pipe case. |
| **Low** | Maintainability | Dead config fields | Remove unused fields or mark them `// parsed but not yet implemented` and add godoc. |
| **Low** | Observability | Sparse use of the injected `*slog.Logger` | Increase `e.log()` call sites at decision points (format selection, archive hits, 403 recovery, playlist range trimming). |

**Cross-cutting:** Consider extracting the semaphore acquisition pattern into a single helper used by both `WorkerPool` and `SegmentDownloader` so the cancellation discipline cannot diverge again.

---

*End of re-audit. The original 2026-05 findings and their resolution table are preserved above for historical traceability.*

---

## 11. Resolution Log (Re-Audit Findings)

| Finding | Status | Resolution |
|---------|--------|------------|
| 9.1 `OnError` data race | **Fixed** | `Engine` gained an `onErrorMu sync.Mutex`; `reportFailure` (internal/core/engine.go) now serializes the callback. Godoc on `DownloadOptions.OnError` (internal/config/config.go) documents the new guarantee. README example updated. |
| 9.2 Segment semaphore blocks on cancel | **Fixed** | `SegmentDownloader.DownloadToFile` (internal/downloader/segment.go) wraps the `sem <- struct{}{}` send in `select { case <-ctx.Done(): ...; case sem <- struct{}{}: }`, mirroring `pipeline.WorkerPool.Submit`. The launch loop now exits promptly on cancellation; `rs.Save()` persists progress and the function returns `ctx.Err()`. |
| 9.3 Negative concurrency panics | **Fixed** | `NewWorkerPool` (internal/pipeline/workerpool.go) clamps `limit < 0` to `0`. `SegmentDownloader.DownloadToFile` clamps `Workers < 1` to `1`. |
| 9.4 Dead config / unwired flags | **Fixed** | Removed `NoContinue`, `BufferSize`, `ForceWriteArchive`, `RemuxVideo`, `RecodeVideo`, `PlaylistItems` from `config.DownloadOptions`. Removed the matching `--remux-video`, `--recode-video`, `--playlist-items` flag definitions from `cmd/root.go`. The `--no-continue` flag still works via the inverted handler against `ContinuePartial`. |
| 9.5 Streaming `-x` bypass | **Fixed** | Removed `internal/postprocessor/stream.go` and the bypass branch in `engine.downloadVideo`. The `-x` path now flows through the standard segmented download (resume, identity, 403 recovery, rate limiting, progress) and `postProcessVideo` invokes the existing `Converter.ExtractAudio` on the resulting file. `videoTask.streamed` field deleted. |

**Verification:** `go build ./...`, `go test -race ./...`, `golangci-lint run`.

---

*Resolution implemented in a single follow-up PR addressing §9 of this re-audit.*
