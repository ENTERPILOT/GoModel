package batch

import (
	"fmt"
)

// usageClaimAttempts bounds the compare-and-swap retries of ClaimUsage. A
// retry is needed only when another write lands between the read and the
// swap, such as a status refresh of the same batch.
const usageClaimAttempts = 5

// claimUsage is the compare-and-swap ClaimUsage runs on a store that keeps
// the batch as one serialized payload: read it, stop when it already records
// usage, otherwise replace it with the claimed batch if it is unchanged since
// the read.
func claimUsage(batch *StoredBatch, read func() ([]byte, error), swap func(current, next []byte) (bool, error)) (bool, error) {
	if err := checkUsageClaim(batch); err != nil {
		return false, err
	}
	next, err := serializeBatch(batch)
	if err != nil {
		return false, err
	}
	for range usageClaimAttempts {
		current, err := read()
		if err != nil {
			return false, err
		}
		stored, err := deserializeBatch(current)
		if err != nil {
			return false, fmt.Errorf("decode batch: %w", err)
		}
		if stored.UsageLoggedAt != nil {
			return false, nil
		}
		swapped, err := swap(current, next)
		if err != nil || swapped {
			return swapped, err
		}
	}
	return false, fmt.Errorf("claim batch usage: batch %s kept changing", batch.Batch.ID)
}

func checkUsageClaim(batch *StoredBatch) error {
	if batch == nil || batch.Batch == nil || batch.Batch.ID == "" {
		return fmt.Errorf("batch id is required")
	}
	if batch.UsageLoggedAt == nil {
		return fmt.Errorf("claim batch usage: batch %s has no usage_logged_at", batch.Batch.ID)
	}
	return nil
}
