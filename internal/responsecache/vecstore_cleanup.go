package responsecache

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const vecStoreCleanupInterval = time.Hour

type vecCleanup struct {
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func startVecCleanup(store VecStore) *vecCleanup {
	c := &vecCleanup{stopCh: make(chan struct{})}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(vecStoreCleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := store.DeleteExpired(context.Background()); err != nil {
					slog.Warn("vecstore: delete expired", "err", err)
				}
			case <-c.stopCh:
				return
			}
		}
	}()
	return c
}

func (c *vecCleanup) close() {
	if c == nil {
		return
	}
	close(c.stopCh)
	c.wg.Wait()
}
