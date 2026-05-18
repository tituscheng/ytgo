// Package format implements yt-dlp-style format selection.
package format

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ytgo/internal/extractor"
)

// Select evaluates the format selector string against the available formats
// and returns the chosen formats. It supports:
//   - Special names: best, worst, bestvideo/bv, worstvideo/wv, bestaudio/ba, worstaudio/wa
//   - Format codes: 22, 18, etc.
//   - Filters: best[height<=720]
//   - Combinations: bv+ba
//   - Fallbacks: bv*+ba/best
func Select(selector string, formats []extractor.Format) ([]extractor.Format, error) {
	if len(formats) == 0 {
		return nil, fmt.Errorf("no formats available")
	}

	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = "best"
	}

	// Handle fallbacks: "expr1/expr2/expr3"
	parts := splitFallbacks(selector)
	for _, part := range parts {
		result, err := selectSingle(part, formats)
		if err == nil && len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no matching formats for selector: %s", selector)
}

func splitFallbacks(s string) []string {
	// We need to be careful not to split on / inside brackets
	var parts []string
	var buf strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '[':
			depth++
			buf.WriteRune(r)
		case ']':
			depth--
			buf.WriteRune(r)
		case '/':
			if depth == 0 {
				parts = append(parts, buf.String())
				buf.Reset()
			} else {
				buf.WriteRune(r)
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		parts = append(parts, buf.String())
	}
	return parts
}

func selectSingle(selector string, formats []extractor.Format) ([]extractor.Format, error) {
	selector = strings.TrimSpace(selector)

	// Handle combinations: "bv+ba"
	if strings.Contains(selector, "+") {
		return selectCombination(selector, formats)
	}

	// Handle filters: "best[height<=720]"
	if idx := strings.Index(selector, "["); idx >= 0 {
		if end := strings.LastIndex(selector, "]"); end > idx {
			base := strings.TrimSpace(selector[:idx])
			filter := selector[idx+1 : end]
			return selectWithFilter(base, filter, formats)
		}
	}

	// Plain selector
	return selectPlain(selector, formats)
}

func selectPlain(selector string, formats []extractor.Format) ([]extractor.Format, error) {
	switch strings.ToLower(selector) {
	case "best", "b":
		f := best(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no best format")
		}
		return []extractor.Format{*f}, nil
	case "worst":
		f := worst(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no worst format")
		}
		return []extractor.Format{*f}, nil
	case "bestvideo", "bv":
		f := bestVideo(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no best video format")
		}
		return []extractor.Format{*f}, nil
	case "worstvideo", "wv":
		f := worstVideo(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no worst video format")
		}
		return []extractor.Format{*f}, nil
	case "bestaudio", "ba":
		f := bestAudio(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no best audio format")
		}
		return []extractor.Format{*f}, nil
	case "worstaudio", "wa":
		f := worstAudio(formats, nil)
		if f == nil {
			return nil, fmt.Errorf("no worst audio format")
		}
		return []extractor.Format{*f}, nil
	}

	// Try format code match
	for _, f := range formats {
		if f.FormatID == selector {
			return []extractor.Format{f}, nil
		}
	}

	// Try extension match
	var matches []extractor.Format
	for _, f := range formats {
		if f.Ext == selector {
			matches = append(matches, f)
		}
	}
	if len(matches) > 0 {
		f := best(matches, nil)
		if f != nil {
			return []extractor.Format{*f}, nil
		}
	}

	return nil, fmt.Errorf("unknown selector: %s", selector)
}

func selectWithFilter(base, filter string, formats []extractor.Format) ([]extractor.Format, error) {
	pred, err := parseFilter(filter)
	if err != nil {
		return nil, err
	}
	return selectPlain(base, filterFormats(formats, pred))
}

func selectCombination(selector string, formats []extractor.Format) ([]extractor.Format, error) {
	parts := strings.Split(selector, "+")
	var result []extractor.Format
	used := make(map[string]bool)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// Remove trailing * (fallback marker)
		part = strings.TrimSuffix(part, "*")
		selected, err := selectSingle(part, formats)
		if err != nil {
			return nil, err
		}
		for _, f := range selected {
			if !used[f.FormatID] {
				result = append(result, f)
				used[f.FormatID] = true
			}
		}
	}
	return result, nil
}

// filter helpers

type predicate func(extractor.Format) bool

func filterFormats(formats []extractor.Format, pred predicate) []extractor.Format {
	var out []extractor.Format
	for _, f := range formats {
		if pred(f) {
			out = append(out, f)
		}
	}
	return out
}

func best(formats []extractor.Format, pred predicate) *extractor.Format {
	var best *extractor.Format
	for i := range formats {
		f := &formats[i]
		if pred != nil && !pred(*f) {
			continue
		}
		if best == nil || score(f) > score(best) {
			best = f
		}
	}
	return best
}

func worst(formats []extractor.Format, pred predicate) *extractor.Format {
	var worst *extractor.Format
	for i := range formats {
		f := &formats[i]
		if pred != nil && !pred(*f) {
			continue
		}
		if worst == nil || score(f) < score(worst) {
			worst = f
		}
	}
	return worst
}

func bestVideo(formats []extractor.Format, pred predicate) *extractor.Format {
	// Prefer video-only, but accept combined if no video-only exists
	var videoOnly []extractor.Format
	var combined []extractor.Format
	for _, f := range formats {
		if !f.HasVideo {
			continue
		}
		if pred != nil && !pred(f) {
			continue
		}
		if f.HasAudio {
			combined = append(combined, f)
		} else {
			videoOnly = append(videoOnly, f)
		}
	}
	if len(videoOnly) > 0 {
		return best(videoOnly, nil)
	}
	return best(combined, nil)
}

func worstVideo(formats []extractor.Format, pred predicate) *extractor.Format {
	var out []extractor.Format
	for _, f := range formats {
		if f.HasVideo {
			out = append(out, f)
		}
	}
	return worst(out, pred)
}

func bestAudio(formats []extractor.Format, pred predicate) *extractor.Format {
	// Prefer audio-only, but accept combined if no audio-only exists
	var audioOnly []extractor.Format
	var combined []extractor.Format
	for _, f := range formats {
		if !f.HasAudio {
			continue
		}
		if pred != nil && !pred(f) {
			continue
		}
		if f.HasVideo {
			combined = append(combined, f)
		} else {
			audioOnly = append(audioOnly, f)
		}
	}
	if len(audioOnly) > 0 {
		return best(audioOnly, nil)
	}
	return best(combined, nil)
}

func worstAudio(formats []extractor.Format, pred predicate) *extractor.Format {
	var out []extractor.Format
	for _, f := range formats {
		if f.HasAudio {
			out = append(out, f)
		}
	}
	return worst(out, pred)
}

// score returns a heuristic quality score. Higher is better.
func score(f *extractor.Format) float64 {
	var s float64
	if f.HasVideo {
		s += float64(f.Height) * 10
		s += f.FPS * 0.5
	}
	if f.HasAudio {
		s += f.ABR * 10
		s += float64(f.AudioChannels) * 50
	}
	// Prefer larger filesize
	if f.Filesize > 0 {
		s += float64(f.Filesize) / 1e6
	}
	return s
}

// parseFilter turns "height<=720" into a predicate.
func parseFilter(filter string) (predicate, error) {
	// Supported ops: =, !=, <, <=, >, >=
	re := regexp.MustCompile(`^\s*(\w+)\s*(=|!=|<=|>=|<|>)\s*(.+?)\s*$`)
	m := re.FindStringSubmatch(filter)
	if m == nil {
		return nil, fmt.Errorf("invalid filter: %s", filter)
	}
	field := m[1]
	op := m[2]
	rawVal := m[3]

	return func(f extractor.Format) bool {
		var val float64
		switch field {
		case "height":
			val = float64(f.Height)
		case "width":
			val = float64(f.Width)
		case "fps":
			val = f.FPS
		case "tbr":
			val = f.TBR
		case "abr":
			val = f.ABR
		case "vbr":
			val = f.VBR
		case "filesize":
			val = float64(f.Filesize)
		case "audio_channels":
			val = float64(f.AudioChannels)
		default:
			return false
		}

		cmpVal, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return false
		}

		switch op {
		case "=":
			return val == cmpVal
		case "!=":
			return val != cmpVal
		case "<":
			return val < cmpVal
		case "<=":
			return val <= cmpVal
		case ">":
			return val > cmpVal
		case ">=":
			return val >= cmpVal
		}
		return false
	}, nil
}
