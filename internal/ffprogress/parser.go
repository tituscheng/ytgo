// Package ffprogress parses ffmpeg machine-readable progress output.
package ffprogress

import (
	"bytes"
	"strconv"
	"strings"
	"time"
)

// Parser is an io.Writer that scans ffmpeg `-progress pipe:1` output
// (newline-separated key=value pairs). Partial lines are buffered across writes.
type Parser struct {
	buf         []byte
	OnOutTime   func(outMs int64) // media time processed so far, in milliseconds
	OnTotalSize func(n int64)     // bytes written so far (total_size)
	OnEnd       func()            // progress=end
}

// NewParser returns a Parser that reports out_time in milliseconds.
func NewParser(onOutTime func(outMs int64)) *Parser {
	return &Parser{OnOutTime: onOutTime}
}

func (p *Parser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := p.buf[:i]
		p.buf = p.buf[i+1:]
		p.handleLine(line)
	}
	return len(b), nil
}

func (p *Parser) handleLine(line []byte) {
	key, val, ok := bytes.Cut(line, []byte{'='})
	if !ok {
		return
	}
	k := string(bytes.TrimSpace(key))
	v := string(bytes.TrimSpace(val))
	switch k {
	case "out_time_us":
		us, err := strconv.ParseInt(v, 10, 64)
		if err != nil || us < 0 || p.OnOutTime == nil {
			return
		}
		p.OnOutTime(us / 1000)
	case "total_size":
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || p.OnTotalSize == nil {
			return
		}
		p.OnTotalSize(n)
	case "progress":
		if v == "end" && p.OnEnd != nil {
			p.OnEnd()
		}
	}
}

// DurationWriter scans ffmpeg stderr for a Duration: HH:MM:SS.xx line.
// After the first match it ignores further writes (aside from accepting them).
type DurationWriter struct {
	buf        []byte
	isFound    bool
	OnDuration func(d time.Duration)
}

func (w *DurationWriter) Write(b []byte) (int, error) {
	if w.isFound {
		return len(b), nil
	}
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		if d, ok := ParseDurationLine(string(line)); ok {
			w.isFound = true
			if w.OnDuration != nil {
				w.OnDuration(d)
			}
			return len(b), nil
		}
	}
	return len(b), nil
}

// ParseDurationLine extracts an ffmpeg Duration: value from a log line.
func ParseDurationLine(line string) (time.Duration, bool) {
	const prefix = "Duration:"
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[idx+len(prefix):])
	// HH:MM:SS.xx, ...
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		end = len(rest)
	}
	raw := rest[:end]
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 {
		return 0, false
	}
	d := time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(sec*float64(time.Second))
	return d, true
}
