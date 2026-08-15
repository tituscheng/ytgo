package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

func collectTracker(plan trackerPlan) (*progressTracker, *[]ytgo.Progress) {
	var events []ytgo.Progress
	tr := newProgressTracker(plan, func(p ytgo.Progress) {
		events = append(events, p)
	})
	return tr, &events
}

func TestProgressTracker_DownloadOnlyMonotonicToOne(t *testing.T) {
	tr, ev := collectTracker(trackerPlan{
		VideoID: "v1",
		Title:   "T",
		Formats: []extractor.Format{{FormatID: "18", Filesize: 100}},
	})
	tr.setDownload("18", 0, 100)
	tr.setDownload("18", 50, 100)
	tr.setDownload("18", 100, 100)
	tr.completeDownload("18")
	tr.finish()

	require.NotEmpty(t, *ev)
	var last float64 = -2
	for _, p := range *ev {
		assert.True(t, p.HasOverall)
		f := p.Fraction()
		assert.GreaterOrEqual(t, f, 0.0)
		assert.GreaterOrEqual(t, f, last)
		last = f
	}
	assert.InDelta(t, 1.0, (*ev)[len(*ev)-1].Fraction(), 1e-9)
}

func TestProgressTracker_DownloadPlusMergeDoesNotReset(t *testing.T) {
	tr, ev := collectTracker(trackerPlan{
		VideoID:   "v1",
		Title:     "T",
		Formats:   []extractor.Format{{FormatID: "18", Filesize: 100}},
		NeedMerge: true,
	})
	tr.setDownload("18", 100, 100)
	tr.completeDownload("18")
	tr.closeDownloadPhase()
	afterDL := (*ev)[len(*ev)-1].Fraction()
	assert.InDelta(t, 1-weightMerge, afterDL, 0.02)

	tr.setStage(stageMerge, ytgo.PhaseMerge, 0, 1000)
	tr.setStage(stageMerge, ytgo.PhaseMerge, 500, 1000)
	tr.completeStage(stageMerge)
	tr.finish()

	var last float64 = -2
	var sawMerge bool
	for _, p := range *ev {
		f := p.Fraction()
		assert.GreaterOrEqual(t, f, last-1e-12)
		last = f
		if p.Phase == ytgo.PhaseMerge {
			sawMerge = true
			assert.GreaterOrEqual(t, f, afterDL-0.01)
		}
	}
	assert.True(t, sawMerge)
	assert.InDelta(t, 1.0, last, 1e-9)
}

func TestProgressTracker_TwoFormatsShareDownloadWeight(t *testing.T) {
	tr, ev := collectTracker(trackerPlan{
		VideoID: "v1",
		Formats: []extractor.Format{
			{FormatID: "v", Filesize: 0},
			{FormatID: "a", Filesize: 0},
		},
	})
	tr.setDownload("v", 40, 100)
	tr.setDownload("a", 40, 100)
	got := (*ev)[len(*ev)-1].Fraction()
	assert.InDelta(t, 0.40, got, 0.02)
}

func TestProgressTracker_HLSEstimateDoesNotGoBackwards(t *testing.T) {
	tr, ev := collectTracker(trackerPlan{
		VideoID: "v1",
		Formats: []extractor.Format{{FormatID: "hls"}},
	})
	tr.setDownload("hls", 50, 100) // 50%
	tr.setDownload("hls", 60, 200) // estimate grew; raw frac 30%
	var last float64 = -2
	for _, p := range *ev {
		f := p.Fraction()
		assert.GreaterOrEqual(t, f, last)
		last = f
	}
	assert.GreaterOrEqual(t, last, 0.49)
}

func TestProgressTracker_SkippedRemuxReachesOne(t *testing.T) {
	tr, ev := collectTracker(trackerPlan{
		VideoID:   "v1",
		Formats:   []extractor.Format{{FormatID: "hls", URL: "https://x/a.m3u8"}},
		NeedRemux: true,
	})
	tr.setDownload("hls", 100, 100)
	tr.completeDownload("hls")
	tr.closeDownloadPhase() // remux reserved but unused
	tr.finish()
	assert.InDelta(t, 1.0, (*ev)[len(*ev)-1].Fraction(), 1e-9)
}

func TestProgressTracker_TerminalMatchesLibrary(t *testing.T) {
	var events []ytgo.Progress
	var suffixes []string
	tr := newProgressTracker(trackerPlan{
		VideoID:   "v1",
		Title:     "T",
		Formats:   []extractor.Format{{FormatID: "18", Filesize: 100}},
		NeedMerge: true,
	}, func(p ytgo.Progress) {
		events = append(events, p)
	})
	tr.paint = func(p ytgo.Progress) {
		suffixes = append(suffixes, formatProgressSuffix(p, defaultPhaseLabel(p.Phase)))
	}

	tr.setDownload("18", 50, 100)
	tr.completeDownload("18")
	tr.setStage(stageMerge, ytgo.PhaseMerge, 100, 1000)
	tr.completeStage(stageMerge)

	require.Equal(t, len(events), len(suffixes))
	for i, p := range events {
		assert.Contains(t, suffixes[i], "["+string(p.Phase)+"]")
		f := p.Fraction()
		if f < 0 {
			assert.NotContains(t, suffixes[i], "%")
			continue
		}
		want := fmt.Sprintf("%.1f%%", f*100)
		assert.Contains(t, suffixes[i], want)
	}
}

func TestFormatProgressSuffix_UsesOverallNotPhaseLocal(t *testing.T) {
	p := ytgo.Progress{
		Phase:      ytgo.PhaseMerge,
		Cur:        0,
		Tot:        1000,
		Overall:    0.94,
		HasOverall: true,
	}
	s := formatProgressSuffix(p, defaultPhaseLabel(p.Phase))
	assert.Contains(t, s, "94.0%")
	assert.NotContains(t, s, "0.0%")
	assert.Contains(t, s, "[merge]")
}

func TestFormatProgressSuffix_Indeterminate(t *testing.T) {
	p := ytgo.Progress{
		Phase:      ytgo.PhaseDownload,
		Overall:    -1,
		HasOverall: true,
	}
	s := formatProgressSuffix(p, defaultPhaseLabel(p.Phase))
	assert.NotContains(t, s, "%")
	assert.Contains(t, s, "[download]")
}
