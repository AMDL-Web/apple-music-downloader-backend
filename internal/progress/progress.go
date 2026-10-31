// Package progress turns the backend's per-stage item progress
// (domain.ItemProgress) into the bars the amdl clients draw: the seven-segment
// stage bar of one track, the album bar of a job, and a remaining-time
// estimate. The API deliberately sends no overall percentage, so the stage
// weighting lives here, shared by the CLI, its TUI and the desktop app, and
// matched against the web console's copy by a test.
package progress

import (
	"time"

	"amdl/internal/domain"
)

// Stage is one segment of the stage bar.
type Stage int

// The pipeline's stages in order. The backend reports a fraction for download
// and decrypt and a done flag for the rest.
const (
	StageResolve Stage = iota
	StageDownload
	StageDecrypt
	StageRemux
	StageVerify
	StageTag
	StageSave
	StageCount
)

// Weights are each stage's share of a track's wall time, summing to 1. They
// are the measured shares of a typical ALAC track on a home connection:
// download dominates and depends on the network, so it takes more of the bar
// on a slow link and less on a fast one, and the remaining stages are a
// sliver each. The web console keeps the same values as STAGE_WEIGHTS in
// apps/console/static/js/lib/status.js; TestWeightsMatchConsole holds them
// together.
var Weights = [StageCount]float64{0.09, 0.77, 0.09, 0.01, 0.02, 0.01, 0.01}

// Stages is how far each stage has come, each in [0,1].
type Stages [StageCount]float64

// Weighted returns the overall fraction the stages stand for under Weights.
func (s Stages) Weighted() float64 {
	total := 0.0
	for i, w := range Weights {
		total += w * s[i]
	}
	return total
}

// Current returns the first stage that is not done, or StageCount when all
// are.
func (s Stages) Current() Stage {
	for i, v := range s {
		if v < 1 {
			return Stage(i)
		}
	}
	return StageCount
}

// statusRank orders the item statuses of a run as the pipeline publishes them,
// so a stage counts as done once the item has moved past it even when its flag
// is false: Verified stays false when download.check_integrity is off, and an
// unmeasurable transfer never reports a fraction. Saving comes before tagging
// because the integrity check runs under saving and the tag write under
// tagging (internal/media#Downloader.finalizeTrackOutput); a rank that put
// them the other way round showed the tag stage done while saving and undone
// again at tagging, which Rate reads as a retry.
var statusRank = map[domain.ItemStatus]int{
	domain.ItemQueued:          0,
	domain.ItemResolving:       1,
	domain.ItemWaitingDownload: 2,
	domain.ItemDownloading:     3,
	domain.ItemWaitingDecrypt:  4,
	domain.ItemDecrypting:      5,
	domain.ItemRemuxing:        6,
	domain.ItemSaving:          7,
	domain.ItemTagging:         8,
	domain.ItemCompleted:       9,
}

// ItemStages returns how far one track has come. A completed or
// skipped_existing track is done in every stage: a skipped one reports only
// Resolved because its file was already on disk. A failed or cancelled track
// keeps the stages it reached.
func ItemStages(it domain.JobItem) Stages {
	if it.Status == domain.ItemCompleted || it.Status == domain.ItemSkipped {
		return Stages{1, 1, 1, 1, 1, 1, 1}
	}
	p := it.Progress
	rank, ok := statusRank[it.Status]
	if !ok {
		rank = -1 // failed or cancelled: flags only
	}
	done := func(flag bool, past domain.ItemStatus) float64 {
		if flag || (rank >= 0 && rank >= statusRank[past]) {
			return 1
		}
		return 0
	}
	frac := func(v float64, past domain.ItemStatus) float64 {
		if rank >= 0 && rank >= statusRank[past] {
			return 1
		}
		return min(max(v, 0), 1)
	}
	return Stages{
		StageResolve:  done(p.Resolved, domain.ItemWaitingDownload),
		StageDownload: frac(p.Download, domain.ItemWaitingDecrypt),
		StageDecrypt:  frac(p.Decrypt, domain.ItemRemuxing),
		StageRemux:    done(p.Remuxed, domain.ItemSaving),
		StageVerify:   done(p.Verified, domain.ItemTagging),
		StageTag:      done(p.Tagged, domain.ItemCompleted),
		StageSave:     done(p.Saved, domain.ItemCompleted),
	}
}

// Indeterminate reports whether a track is in a measured stage that has no
// fraction yet: downloading or decrypting at 0, which means "running, size
// unknown" rather than "no bytes yet".
func Indeterminate(it domain.JobItem) bool {
	switch it.Status {
	case domain.ItemDownloading:
		return it.Progress.Download == 0
	case domain.ItemDecrypting:
		return it.Progress.Decrypt == 0
	}
	return false
}

// JobStages averages the stages of a job's tracks; tracks the job has not
// listed yet count as not started. Without items it falls back to the job's
// counters, which are all a list payload carries.
func JobStages(j domain.Job, items []domain.JobItem) Stages {
	var s Stages
	total := max(j.TotalItems, len(items))
	if total == 0 {
		if j.Status == domain.JobCompleted {
			return Stages{1, 1, 1, 1, 1, 1, 1}
		}
		return s
	}
	if len(items) == 0 {
		done := float64(j.CompletedItems+j.SkippedItems) / float64(total)
		for i := range s {
			s[i] = done
		}
		return s
	}
	for _, it := range items {
		is := ItemStages(it)
		for i := range s {
			s[i] += is[i]
		}
	}
	for i := range s {
		s[i] /= float64(total)
	}
	return s
}

// rateWindow is how far back the samples behind the remaining-time estimate
// reach.
const rateWindow = 30 * time.Second

// Rate estimates the time left from how fast a job's progress moved over the
// last rateWindow. The zero value is ready to use.
type Rate struct {
	samples []sample
}

type sample struct {
	at       time.Time
	progress float64
}

// Add records a job's progress at a moment and drops samples older than the
// window, keeping the oldest one inside it as the rate's base. Progress that
// went back (a retry) starts over.
func (r *Rate) Add(at time.Time, progress float64) {
	if n := len(r.samples); n > 0 && progress < r.samples[n-1].progress {
		r.samples = r.samples[:0]
	}
	r.samples = append(r.samples, sample{at, progress})
	cut := 0
	for cut < len(r.samples)-2 && at.Sub(r.samples[cut+1].at) > rateWindow {
		cut++
	}
	r.samples = r.samples[cut:]
}

// Remaining returns the estimated time left, or 0 when there is not enough to
// go on.
func (r *Rate) Remaining() time.Duration {
	if len(r.samples) < 2 {
		return 0
	}
	first, last := r.samples[0], r.samples[len(r.samples)-1]
	dt := last.at.Sub(first.at)
	dp := last.progress - first.progress
	if dt < 3*time.Second || dp <= 0 || last.progress >= 1 {
		return 0
	}
	return time.Duration(float64(dt) * (1 - last.progress) / dp)
}
