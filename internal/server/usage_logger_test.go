package server

import (
	"sync"

	"gomodel/internal/usage"
)

type usageCaptureLogger struct {
	mu      sync.Mutex
	config  usage.Config
	entries []*usage.UsageEntry
}

func (l *usageCaptureLogger) Write(entry *usage.UsageEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *usageCaptureLogger) Config() usage.Config { return l.config }
func (l *usageCaptureLogger) Close() error         { return nil }

func (l *usageCaptureLogger) Entries() []*usage.UsageEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries := make([]*usage.UsageEntry, len(l.entries))
	copy(entries, l.entries)
	return entries
}
