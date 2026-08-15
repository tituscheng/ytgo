package ffprogress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParser(t *testing.T) {
	var got []int64
	var sizes []int64
	var ended bool
	p := &Parser{
		OnOutTime:   func(ms int64) { got = append(got, ms) },
		OnTotalSize: func(n int64) { sizes = append(sizes, n) },
		OnEnd:       func() { ended = true },
	}

	// Split a typical -progress block across two writes, including a partial
	// line, to exercise cross-write buffering.
	_, _ = p.Write([]byte("frame=10\nout_time_us=1500000\nspeed=2x\nout_time_us=30000"))
	_, _ = p.Write([]byte("00\ntotal_size=4096\nprogress=continue\n"))

	require.Equal(t, []int64{1500, 3000}, got)
	require.Equal(t, []int64{4096}, sizes)
	assert.False(t, ended)

	got = nil
	_, _ = p.Write([]byte("out_time_us=N/A\nbitrate=128k\nprogress=end\n"))
	require.Empty(t, got)
	assert.True(t, ended)
}

func TestParseDurationLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   time.Duration
		wantOK bool
	}{
		{
			name:   "typical info line",
			line:   "  Duration: 00:10:15.12, start: 0.000000, bitrate: 1234 kb/s",
			want:   10*time.Minute + 15*time.Second + 120*time.Millisecond,
			wantOK: true,
		},
		{
			name:   "hours",
			line:   "Duration: 01:02:03.50",
			want:   time.Hour + 2*time.Minute + 3500*time.Millisecond,
			wantOK: true,
		},
		{
			name:   "no duration",
			line:   "Stream #0:0: Video: h264",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseDurationLine(tt.line)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestDurationWriter(t *testing.T) {
	var got time.Duration
	w := &DurationWriter{OnDuration: func(d time.Duration) { got = d }}
	_, _ = w.Write([]byte("Input #0, mpegts\n  Duration: 00:00:05.50, start: 0\n"))
	assert.Equal(t, 5500*time.Millisecond, got)

	// Second duration is ignored.
	_, _ = w.Write([]byte("Duration: 01:00:00.00\n"))
	assert.Equal(t, 5500*time.Millisecond, got)
}
