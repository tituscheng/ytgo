package innertube

import (
	"net/url"
	"strconv"
	"strings"
)

// mergeStreaming combines player streaming data the way yt-dlp nightly does,
// with one extra step:
//
//  1. Prefer the quality client (VISIONOS / TV) — adaptive HTTPS works without
//     a GVS PO token today.
//  2. Always keep ANDROID_VR muxed progressive (itag 18/22/17).
//  3. Skip ANDROID_VR adaptive HTTPS/DASH when the quality client already
//     supplied adaptive streams (those VR URLs 403 without a PO token).
//  4. If the quality client is empty (SABR-only or failed), keep ANDROID_VR
//     adaptive as a last resort — enforcement is still intermittent.
//
// Same itag with different AudioTrack / xtags (YouTube auto-dubs) are distinct
// streams. Deduping on itag alone keeps the first language — often a dub —
// and drops the original.
//
// Formats without a direct URL (signatureCipher / SABR) are dropped: ytgo
// has no JS n-sig solver.
func mergeStreaming(vr, quality *StreamingData) StreamingData {
	var out StreamingData
	seen := map[string]struct{}{}

	add := func(src []Format, dest *[]Format) {
		for _, f := range src {
			if !usableFormat(f) {
				continue
			}
			key := audioVariantKey(f)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			*dest = append(*dest, f)
		}
	}

	if quality != nil {
		add(quality.Formats, &out.Formats)
		add(quality.AdaptiveFormats, &out.AdaptiveFormats)
		out.HlsManifestURL = quality.HlsManifestURL
		out.DashManifestURL = quality.DashManifestURL
	}

	qualityHasAdaptive := len(out.AdaptiveFormats) > 0
	if vr != nil {
		add(vr.Formats, &out.Formats)
		if !qualityHasAdaptive {
			add(vr.AdaptiveFormats, &out.AdaptiveFormats)
		}
		if out.HlsManifestURL == "" {
			out.HlsManifestURL = vr.HlsManifestURL
		}
		// ANDROID_VR DASH manifests also need a GVS PO token; only take them
		// when nothing else is available.
		if out.DashManifestURL == "" && !qualityHasAdaptive {
			out.DashManifestURL = vr.DashManifestURL
		}
	}
	return out
}

// audioVariantKey distinguishes dubbed vs original copies of the same itag.
func audioVariantKey(f Format) string {
	id := strconv.Itoa(f.ItagNo)
	if f.AudioTrack != nil {
		if tid := strings.TrimSpace(f.AudioTrack.ID); tid != "" {
			return id + ":" + tid
		}
	}
	if xt := xtagsOf(f.URL); xt != "" {
		return id + ":" + xt
	}
	return id
}

func xtagsOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get("xtags")
}

func usableFormat(f Format) bool {
	if strings.TrimSpace(f.URL) == "" {
		return false
	}
	return strings.TrimSpace(f.Cipher) == ""
}

func isPlayable(resp *PlayerResponse) bool {
	return resp != nil && resp.PlayabilityStatus.Status == "OK"
}

func isPrivate(resp *PlayerResponse) bool {
	if resp == nil || resp.PlayabilityStatus.Status != "LOGIN_REQUIRED" {
		return false
	}
	return strings.HasPrefix(resp.PlayabilityStatus.Reason, "This video is private")
}

func isAgeRestricted(resp *PlayerResponse) bool {
	if resp == nil || resp.PlayabilityStatus.Status != "LOGIN_REQUIRED" {
		return false
	}
	return !isPrivate(resp)
}

func isUnplayable(resp *PlayerResponse) bool {
	if resp == nil {
		return false
	}
	switch resp.PlayabilityStatus.Status {
	case "UNPLAYABLE", "ERROR":
		return true
	}
	return false
}
