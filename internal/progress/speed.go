package progress

import (
	"time"

	"amdl/internal/domain"
)

// Transferred is the bytes a job's run has moved so far: the sizes of the
// tracks written plus what the tracks still in flight have downloaded
// (download_bytes), so an average speed has a value from the first bytes.
func Transferred(items []domain.JobItem) int64 {
	var n int64
	for _, it := range items {
		if it.Status == domain.ItemCompleted {
			n += it.FileSize
		} else {
			n += it.Progress.DownloadBytes
		}
	}
	return n
}

// AvgSpeed is the bytes moved per second of a job's run, until now or until
// it finished: Transferred over the time since it started. The API reports
// no live transfer rate, so this is an average. 0 when unknown.
func AvgSpeed(j domain.Job, items []domain.JobItem, now time.Time) float64 {
	if j.StartedAt == nil {
		return 0
	}
	if j.FinishedAt != nil {
		now = *j.FinishedAt
	}
	elapsed := now.Sub(*j.StartedAt).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(Transferred(items)) / elapsed
}
