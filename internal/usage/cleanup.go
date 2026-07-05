package usage

import "time"

// CleanupInterval is how often the cleanup goroutine runs to delete old usage entries.
const CleanupInterval = 1 * time.Hour
