package media

import (
	"math"

	"amdl/internal/domain"
)

// publishStage publishes one item state change: the status/message transition,
// plus an optional mutation of the item's progress breakdown. Entering a stage
// and finishing it are separate calls, because they carry different facts —
// a nil mutate means "this stage has started", and the mark helpers below are
// only passed once the work they name has actually succeeded.
type publishStage func(status domain.ItemStatus, message string, mutate func(*domain.ItemProgress))

func markResolved(p *domain.ItemProgress) { p.Resolved = true }

func markRemuxed(p *domain.ItemProgress) { p.Remuxed = true }

func markVerified(p *domain.ItemProgress) { p.Verified = true }

func markTagged(p *domain.ItemProgress) { p.Tagged = true }

func markSaved(p *domain.ItemProgress) { p.Saved = true }

// startDownload and startDecrypt are the stage-entry mutators. Entering a stage
// restates what is known rather than only adding to it, so a retry within one
// codec corrects a meter the previous attempt left behind — the codec-fallback
// boundary has ResetForAttempt, but a retry inside a codec does not pass
// through it. Reaching decrypt also means the transfer finished, whether or not
// the download meter got to say so (an unmeasurable response never reports a
// fraction), so pin it here rather than leave it stale until the item completes.
func startDownload(p *domain.ItemProgress) { p.Download, p.DownloadBytes = 0, 0 }

func startDecrypt(p *domain.ItemProgress) {
	p.Download = 1
	p.Decrypt = 0
}

// downloadProgress and decryptFrac move one meter. Fractions outside [0,1] are
// clamped rather than rejected: a transfer whose reported total turns out to be
// short should saturate the bar, not push it past full. downloadProgress also
// records the bytes the transfer holds, which is what the fraction is a share
// of.
func downloadProgress(bytes int64, frac float64) func(*domain.ItemProgress) {
	return func(p *domain.ItemProgress) { p.Download, p.DownloadBytes = clampFrac(frac), max(bytes, 0) }
}

func decryptFrac(v float64) func(*domain.ItemProgress) {
	return func(p *domain.ItemProgress) { p.Decrypt = clampFrac(v) }
}

func clampFrac(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// stagesChanged reports whether a one-shot pipeline stage completed. These are
// state transitions, not progress within a state, so they are never rate
// limited (see the two gates documented on trackRun.stageSetter).
func stagesChanged(prev, next domain.ItemProgress) bool {
	return prev.Resolved != next.Resolved || prev.Remuxed != next.Remuxed ||
		prev.Verified != next.Verified || prev.Tagged != next.Tagged || prev.Saved != next.Saved
}

// metersChanged reports whether either continuous meter landed on a different
// whole percent. This is the only thing the time gate may delay.
func metersChanged(prev, next domain.ItemProgress) bool {
	return math.Round(prev.Download*100) != math.Round(next.Download*100) ||
		math.Round(prev.Decrypt*100) != math.Round(next.Decrypt*100)
}
