package auditlog

import "time"

// CleanupInterval is how often the cleanup goroutine runs to delete old log entries.
const CleanupInterval = 1 * time.Hour
