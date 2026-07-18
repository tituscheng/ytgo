package downloader

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	httpStatusRe = regexp.MustCompile(`(?i)HTTP(?:\s+error)?\s+(\d{3})(?:\s+([A-Za-z][A-Za-z \-]*[A-Za-z]))?`)
	openInputRe  = regexp.MustCompile(`(?i)Error opening input file\s+(\S+)`)
	anyHTTPSRe   = regexp.MustCompile(`https://[^\s\]"']+`)
)

// SummarizeStreamError collapses multi-line ffmpeg/CDN noise into one line for CLI output.
// Example: "HTTP 504 Gateway Time-out (vod3.cf.dmcdn.net …/manifest.m3u8)"
func SummarizeStreamError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	lower := strings.ToLower(raw)

	status := ""
	if m := httpStatusRe.FindStringSubmatch(raw); m != nil {
		status = m[1]
		if desc := strings.TrimSpace(m[2]); desc != "" {
			status += " " + strings.TrimSpace(desc)
		}
	} else if strings.Contains(lower, "5xx") || strings.Contains(raw, "Server returned 5") {
		status = "5xx Server Error"
	} else if strings.Contains(lower, "timeout awaiting response headers") ||
		strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "client.timeout exceeded") ||
		(strings.Contains(lower, "timeout") && !strings.Contains(lower, "deadline")) {
		status = "timeout"
	}

	u := ""
	if m := openInputRe.FindStringSubmatch(raw); m != nil {
		u = strings.TrimRight(m[1], ".,;\"")
	} else if m := anyHTTPSRe.FindString(raw); m != "" {
		u = strings.TrimRight(m, ".,;\"")
	}

	if status != "" && u != "" {
		if status == "timeout" {
			return "timeout (" + shortStreamURL(u) + ")"
		}
		return "HTTP " + status + " (" + shortStreamURL(u) + ")"
	}
	if status != "" {
		if status == "timeout" {
			return "CDN timeout"
		}
		return "HTTP " + status
	}

	// Single-line collapse of whatever remains.
	s := strings.Join(strings.Fields(strings.ReplaceAll(raw, "\n", " ")), " ")
	if u != "" {
		// Prefer host/path even when we could not classify the error kind.
		return shortStreamURL(u) + ": " + trimMiddle(s, 100)
	}
	return trimMiddle(s, 160)
}

func trimMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func shortStreamURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if len(raw) > 80 {
			return raw[:80] + "…"
		}
		return raw
	}
	path := u.Path
	// Prefer the meaningful tail (…/aac_q2_0/manifest.m3u8).
	if i := strings.LastIndex(path, "/video/"); i >= 0 {
		path = path[i:]
	} else if len(path) > 48 {
		path = "…" + path[len(path)-48:]
	}
	return u.Host + path
}

// IsTransientStreamError reports whether a stream download error is worth retrying.
// Exported for engine-level fallback decisions (e.g. skip useless FFmpeg after native 504).
func IsTransientStreamError(err error) bool {
	return isTransientStreamError(err)
}
