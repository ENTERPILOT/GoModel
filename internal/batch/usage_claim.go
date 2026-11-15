package batch

import "time"

// The SQL and MongoDB stores keep the usage claim beside the serialized
// batch, as usage_claimed_at and usage_logged_at in Unix nanoseconds.
// ClaimUsage and CompleteUsage write them. Update replaces the serialized
// batch and never clears either, so it cannot undo a claim it never saw; it
// sets usage_logged_at when the batch it saves carries a marker, as after a
// failed CompleteUsage, so that a copy read earlier cannot drop the marker.
//
// A batch logged by an earlier release carries its marker inside the
// serialized batch alone; it is still honoured.

// withUsageLoggedAt overlays the stored usage marker on a decoded batch.
func withUsageLoggedAt(batch *StoredBatch, loggedAt *int64) *StoredBatch {
	if loggedAt != nil {
		ts := time.Unix(0, *loggedAt).UTC()
		batch.UsageLoggedAt = &ts
	}
	return batch
}

// staleUsageClaim is the newest claim time that timeout has made stale at now.
func staleUsageClaim(now time.Time, timeout time.Duration) int64 {
	return now.Add(-timeout).UnixNano()
}
