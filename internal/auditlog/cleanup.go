package auditlog

import "time"

// CleanupInterval is how often the cleanup goroutine runs to delete old log entries.
const CleanupInterval = 1 * time.Hour

// cleanupLockKey is the PostgreSQL advisory lock that keeps the audit log
// retention sweep to one replica at a time (see storage.SingleReplicaSweep).
const cleanupLockKey int64 = 0x676d72742d617564 // "gmrt-aud"
