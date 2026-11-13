package batch

import "time"

// The SQL and MongoDB stores keep the usage claim beside the serialized
// batch, as usage_claimed_at and usage_logged_at in Unix nanoseconds, and
// write both only from ClaimUsage and CompleteUsage. Update replaces the
// serialized batch alone, so it cannot undo a claim it never saw.
//
// A batch logged by an earlier release carries its marker inside the
// serialized batch instead; it is still honoured, and can only ever be set.

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
