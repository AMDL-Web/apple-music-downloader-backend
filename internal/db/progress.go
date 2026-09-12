package db

import (
	"context"

	"amdl/internal/domain"
)

// CountItemProgress uses the same status buckets as domain.CountItemProgress
// without loading item metadata. Use ListItems when a response also needs the
// items, so its counters can be derived from that exact snapshot instead.
func (s *Store) CountItemProgress(ctx context.Context, jobID string) (done, failed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status IN (?,?) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END),0)
		FROM job_items WHERE job_id=?`,
		string(domain.ItemCompleted), string(domain.ItemSkipped), string(domain.ItemFailed), jobID).Scan(&done, &failed)
	return
}
