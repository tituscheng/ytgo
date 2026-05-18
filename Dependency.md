# ytgo Dependency Analysis

This document explains why each external dependency is included, what it costs us, and what alternatives were considered.

---

## `github.com/kkdai/youtube/v2` — YouTube extraction

### Why it was chosen

The project started with a custom WEB Innertube client (hardcoded API key `AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8`). It failed within days with HTTP 400 / `UNPLAYABLE` for most videos. YouTube's WEB client is aggressively protected against programmatic access:

- **Bot detection** — Requires valid `visitorData`, consent cookies, and a matching User-Agent. All rotate and expire.
- **Signature cipher** — Many formats have an `s` parameter that must be decrypted by executing YouTube's player JavaScript.
- **Age restriction** — WEB client returns `LOGIN_REQUIRED` for age-gated content.
- **Throttling** — The `n` query parameter must be transformed via JS function or downloads throttle to ~50 KB/s.

`kkdai/youtube/v2` solved these problems by using the **AndroidVR Innertube client**, which:
- Returns pre-decrypted URLs (no signature cipher to solve)
- Bypasses many age-restriction checks
- Has stable, baked-in API keys
- Does not trigger the same bot-detection heuristics

### What it costs us

The library pulls in `github.com/dop251/goja` (a JavaScript engine) and ~5 transitive dependencies. After source-code analysis of `kkdai/youtube/v2@v2.10.6`, this JS engine is **unnecessary for the AndroidVR client path**:

- **`s` parameter deciphering** is done in **pure Go** via regex parsing of the JS to extract reverse/splice/swap string operations. No JS engine needed.
- **`n` parameter (throttling) decoding** uses `goja` to execute a JS function extracted from YouTube's player code.

The critical issue: in `client.go:GetStreamURLContext` the library runs `unThrottle()` (which calls `goja`) on **every** URL, including AndroidVR URLs. It does this because the `AndroidVRClient` does not set `AndroidVersion > 0`, so a guard clause is skipped. In practice, AndroidVR Innertube URLs work fine without n-sig manipulation — yt-dlp's AndroidVR extractor does not perform n-sig decoding.

### Dependencies eliminated if removed

| Dependency | Type | Purpose |
|---|---|---|
| `github.com/kkdai/youtube/v2` | Direct | YouTube extraction wrapper |
| `github.com/dop251/goja` | Indirect | JS engine for n-sig decode |
| `github.com/bitly/go-simplejson` | Indirect | JSON helper for playlist parsing |
| `github.com/dlclark/regexp2` | Indirect | goja dependency |
| `github.com/go-sourcemap/sourcemap` | Indirect | goja dependency |
| `github.com/google/pprof` | Indirect | goja dependency |

Net savings: ~3 direct deps, ~5+ indirect deps, ~2 MB binary size.

### Plan for removal

Replace with a custom, minimal Innertube client (`internal/extractor/youtube/innertube/`) that:
- Uses only the Go standard library (`net/http`, `encoding/json`)
- Targets the **ANDROID_VR** Innertube client exclusively
- Skips n-sig and signature deciphering entirely (not needed for this client)
- Parses the `/youtubei/v1/player` JSON response directly
- Falls back to `WEB_EMBEDDED_PLAYER` for age-restricted content

See `Future.md` and the project roadmap for implementation status.

---

## `github.com/spf13/cobra` — CLI framework

**Why:** Command tree, flag parsing, help generation, shell completion, and subcommand structure. ytgo has 30+ flags and multiple subcommands (`version`).

**Cost:** Moderate. Well-maintained, standard in the Go ecosystem. No JS engines or heavy transitive deps.

**Alternatives considered:** `urfave/cli` (v3), manual `flag` package. Cobra wins on ecosystem maturity and completion support.

---

## `github.com/spf13/viper` — Configuration management

**Why:** Layered config precedence (flag > env > file > default), YAML/JSON/TOML support, `BindPFlag` integration with Cobra, hot reload.

**Cost:** Moderate. Pulls in `mapstructure`, `fsnotify`, `cast`, `afero`. All are lightweight Go libraries.

**Alternatives considered:** Manual file parsing with `gopkg.in/yaml.v3` only. Viper wins on the binding layer with Cobra flags.

---

## `github.com/fatih/color` — Terminal colors

**Why:** Cross-platform ANSI color codes for progress output, errors, warnings. Used in `engine.go` and `cmd/root.go`.

**Cost:** Minimal. Single file, no transitive dependencies beyond `mattn/go-colorable` and `mattn/go-isatty`.

**Alternatives considered:** Manual ANSI escape codes. The `color` package handles Windows compatibility automatically.

---

## `github.com/briandowns/spinner` — Progress spinners

**Why:** Animated spinner during downloads. Integrates with `fatih/color`.

**Cost:** Minimal. Small library, same `go-isatty` dependency as `color`.

**Alternatives considered:** Manual `\r` progress bar. Spinner is more polished and handles terminal width correctly.

---

## `github.com/stretchr/testify` — Testing (dev-only)

**Why:** `assert`, `require`, `suite` packages for readable tests. Used extensively across the codebase.

**Cost:** Zero runtime cost. Not included in the release binary.

**Alternatives considered:** Standard `testing` package only. Testify wins on readability and failure messages.
