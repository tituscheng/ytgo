package core

import (
	"sync"

	"github.com/briandowns/spinner"

	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/pkg/ytgo"
)

// stageKind is a planned pipeline step. Unknown is the iota-0 sentinel.
type stageKind int

const (
	stageUnknown stageKind = iota
	stageDownload
	stageRemux
	stageMerge
	stageAudio
	stageEmbed
)

const (
	weightMerge          = 0.06
	weightAudioCopy      = 0.04
	weightAudioTranscode = 0.12
	weightEmbed          = 0.02
	weightRemux          = 0.04
)

type formatFrac struct {
	cur    int64
	tot    int64
	weight float64 // share of the download stage, sums to 1
	isDone bool
}

type trackerPlan struct {
	VideoID   string
	Title     string
	Formats   []extractor.Format
	NeedRemux bool
	NeedMerge bool
	NeedAudio bool
	AudioCopy bool
	NeedEmbed bool
}

// progressTracker maps phase-local (cur, tot) into one monotonic video-wide
// [0,1] and emits the same ytgo.Progress to the library callback and the
// terminal renderer.
type progressTracker struct {
	mu sync.Mutex

	videoID string
	title   string

	stages  []stageKind
	weights map[stageKind]float64

	formats map[string]*formatFrac

	stageCur  map[stageKind]int64
	stageTot  map[stageKind]int64
	stageDone map[stageKind]bool

	lastOverall float64
	hasValue    bool
	isRemuxSeen bool

	emit  func(ytgo.Progress)
	paint func(ytgo.Progress)
	spin  *spinner.Spinner
}

func newProgressTracker(plan trackerPlan, emit func(ytgo.Progress)) *progressTracker {
	t := &progressTracker{
		videoID:   plan.VideoID,
		title:     plan.Title,
		weights:   make(map[stageKind]float64),
		formats:   make(map[string]*formatFrac),
		stageCur:  make(map[stageKind]int64),
		stageTot:  make(map[stageKind]int64),
		stageDone: make(map[stageKind]bool),
		emit:      emit,
	}

	post := 0.0
	add := func(k stageKind, w float64) {
		t.stages = append(t.stages, k)
		t.weights[k] = w
		post += w
	}
	if plan.NeedRemux {
		add(stageRemux, weightRemux)
	}
	if plan.NeedMerge {
		add(stageMerge, weightMerge)
	}
	if plan.NeedAudio {
		if plan.AudioCopy {
			add(stageAudio, weightAudioCopy)
		} else {
			add(stageAudio, weightAudioTranscode)
		}
	}
	if plan.NeedEmbed {
		add(stageEmbed, weightEmbed)
	}
	if post > 0.5 {
		// Keep download as the majority of the bar.
		scale := 0.5 / post
		for k, w := range t.weights {
			t.weights[k] = w * scale
		}
		post = 0.5
	}

	t.stages = append([]stageKind{stageDownload}, t.stages...)
	t.weights[stageDownload] = 1 - post

	ids := make([]string, 0, len(plan.Formats))
	sizes := make([]int64, 0, len(plan.Formats))
	var sizeSum int64
	var allSized = len(plan.Formats) > 0
	for _, f := range plan.Formats {
		ids = append(ids, f.FormatID)
		sz := f.Filesize
		if sz <= 0 {
			sz = f.FilesizeApprox
		}
		sizes = append(sizes, sz)
		if sz <= 0 {
			allSized = false
		} else {
			sizeSum += sz
		}
	}
	if len(ids) == 0 {
		ids = []string{""}
		sizes = []int64{0}
		allSized = false
	}
	for i, id := range ids {
		w := 1.0 / float64(len(ids))
		if allSized && sizeSum > 0 {
			w = float64(sizes[i]) / float64(sizeSum)
		}
		t.formats[id] = &formatFrac{weight: w}
	}
	return t
}

func (t *progressTracker) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	s := t.spin
	t.spin = nil
	t.mu.Unlock()
	if s != nil {
		s.Stop()
	}
}

func (t *progressTracker) slot(formatID string) *formatFrac {
	if ff, ok := t.formats[formatID]; ok {
		return ff
	}
	for _, ff := range t.formats {
		if !ff.isDone {
			t.formats[formatID] = ff
			return ff
		}
	}
	for _, ff := range t.formats {
		t.formats[formatID] = ff
		return ff
	}
	ff := &formatFrac{weight: 1}
	t.formats[formatID] = ff
	return ff
}

func (t *progressTracker) aliasFormat(from, to string) {
	if t == nil || from == to {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ff, ok := t.formats[from]; ok {
		t.formats[to] = ff
	}
}

func (t *progressTracker) setDownload(formatID string, cur, tot int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ff := t.slot(formatID)
	ff.cur = cur
	if tot > 0 {
		ff.tot = tot
	}
	t.fireLocked(ytgo.PhaseDownload, formatID, cur, tot)
}

func (t *progressTracker) completeDownload(formatID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ff := t.slot(formatID)
	ff.isDone = true
	if ff.tot > 0 {
		ff.cur = ff.tot
	} else if ff.cur > 0 {
		ff.tot = ff.cur
	}
	t.fireLocked(ytgo.PhaseDownload, formatID, ff.cur, ff.tot)
}

func (t *progressTracker) setStage(kind stageKind, phase ytgo.Phase, cur, tot int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureStageLocked(kind)
	if kind == stageRemux {
		t.isRemuxSeen = true
	}
	t.stageCur[kind] = cur
	if tot > 0 {
		t.stageTot[kind] = tot
	}
	t.fireLocked(phase, "", cur, tot)
}

func (t *progressTracker) completeStage(kind stageKind) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completeStageLocked(kind)
	phase := phaseForStage(kind)
	cur, tot := t.stageCur[kind], t.stageTot[kind]
	if tot <= 0 {
		cur, tot = 1, 1
	} else {
		cur = tot
	}
	t.fireLocked(phase, "", cur, tot)
}

func (t *progressTracker) skipStage(kind stageKind) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.weights[kind] <= 0 || t.stageDone[kind] {
		return
	}
	t.completeStageLocked(kind)
}

func (t *progressTracker) closeDownloadPhase() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ff := range t.formats {
		ff.isDone = true
	}
	if t.weights[stageRemux] > 0 && !t.isRemuxSeen && !t.stageDone[stageRemux] {
		t.completeStageLocked(stageRemux)
	}
}

func (t *progressTracker) finish() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ff := range t.formats {
		ff.isDone = true
	}
	for _, k := range t.stages {
		t.completeStageLocked(k)
	}
	if t.hasValue && t.lastOverall >= 1 {
		return
	}
	t.lastOverall = 1
	t.hasValue = true
	t.fireLocked(t.lastPhaseLocked(), "", 1, 1)
}

func (t *progressTracker) completeStageLocked(kind stageKind) {
	if t.weights[kind] <= 0 {
		t.stageDone[kind] = true
		return
	}
	t.stageDone[kind] = true
	if t.stageTot[kind] > 0 {
		t.stageCur[kind] = t.stageTot[kind]
	}
}

func (t *progressTracker) ensureStageLocked(kind stageKind) {
	if t.weights[kind] > 0 || t.stageDone[kind] {
		return
	}
	hint := defaultWeight(kind)
	budget := 1 - t.lastOverall
	if !t.hasValue {
		budget = 1
	}
	if budget <= 0 {
		t.weights[kind] = 0
		t.stages = append(t.stages, kind)
		return
	}
	take := hint
	if take > budget*0.5 {
		take = budget * 0.5
	}
	var shrinkable float64
	for k, w := range t.weights {
		if k != kind && !t.stageDone[k] {
			shrinkable += w
		}
	}
	if shrinkable > 0 && take > 0 {
		scale := (shrinkable - take) / shrinkable
		if scale < 0 {
			scale = 0
		}
		for k, w := range t.weights {
			if k != kind && !t.stageDone[k] {
				t.weights[k] = w * scale
			}
		}
	}
	t.weights[kind] = take
	t.stages = append(t.stages, kind)
}

func (t *progressTracker) fireLocked(phase ytgo.Phase, formatID string, cur, tot int64) {
	overall, known := t.computeLocked()
	if known {
		if t.hasValue && overall < t.lastOverall {
			overall = t.lastOverall
		}
		t.lastOverall = overall
		t.hasValue = true
	} else {
		overall = -1
	}
	p := ytgo.Progress{
		VideoID:    t.videoID,
		Title:      t.title,
		Phase:      phase,
		FormatID:   formatID,
		Cur:        cur,
		Tot:        tot,
		Overall:    overall,
		HasOverall: true,
	}
	if t.emit != nil {
		t.emit(p)
	}
	if t.paint != nil {
		t.paint(p)
	}
}

func (t *progressTracker) computeLocked() (float64, bool) {
	var sum float64
	var known bool

	if w := t.weights[stageDownload]; w > 0 {
		var frac float64
		var downloadKnown bool
		for _, ff := range t.formats {
			switch {
			case ff.isDone:
				frac += ff.weight
				downloadKnown = true
			case ff.tot > 0:
				f := float64(ff.cur) / float64(ff.tot)
				if f > 1 {
					f = 1
				}
				if f < 0 {
					f = 0
				}
				frac += ff.weight * f
				downloadKnown = true
			}
		}
		if downloadKnown {
			sum += w * frac
			known = true
		}
	}

	for _, k := range t.stages {
		if k == stageDownload {
			continue
		}
		w := t.weights[k]
		if w <= 0 {
			continue
		}
		switch {
		case t.stageDone[k]:
			sum += w
			known = true
		case t.stageTot[k] > 0:
			f := float64(t.stageCur[k]) / float64(t.stageTot[k])
			if f > 1 {
				f = 1
			}
			if f < 0 {
				f = 0
			}
			sum += w * f
			known = true
		}
	}

	if !known {
		return -1, false
	}
	if sum > 1 {
		sum = 1
	}
	if sum < 0 {
		sum = 0
	}
	return sum, true
}

func (t *progressTracker) lastPhaseLocked() ytgo.Phase {
	for i := len(t.stages) - 1; i >= 0; i-- {
		if t.weights[t.stages[i]] > 0 {
			return phaseForStage(t.stages[i])
		}
	}
	return ytgo.PhaseDownload
}

func (t *progressTracker) stageCB(kind stageKind, phase ytgo.Phase, totMs int64) func(outMs int64) {
	if t == nil {
		return nil
	}
	return func(outMs int64) {
		t.setStage(kind, phase, outMs, totMs)
	}
}

func defaultWeight(k stageKind) float64 {
	switch k {
	case stageRemux:
		return weightRemux
	case stageMerge:
		return weightMerge
	case stageAudio:
		return weightAudioCopy
	case stageEmbed:
		return weightEmbed
	default:
		return 0.04
	}
}

func phaseForStage(k stageKind) ytgo.Phase {
	switch k {
	case stageRemux:
		return ytgo.PhaseRemux
	case stageMerge:
		return ytgo.PhaseMerge
	case stageAudio:
		return ytgo.PhaseAudio
	case stageEmbed:
		return ytgo.PhaseEmbed
	default:
		return ytgo.PhaseDownload
	}
}
