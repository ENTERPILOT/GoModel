package usage

import "time"

// CleanupInterval is how often the cleanup goroutine runs to delete old usage entries.
const CleanupInterval = 1 * time.Hour

// cleanupLockKey is the PostgreSQL advisory lock that keeps the usage
// retention sweep to one replica at a time (see storage.SingleReplicaSweep).
const cleanupLockKey int64 = 0x676d72742d757367 // "gmrt-usg"
