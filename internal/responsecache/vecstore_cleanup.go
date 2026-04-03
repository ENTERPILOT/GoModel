package responsecache

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const vecStoreCleanupInterval = time.Hour

type vecCleanup struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func startVecCleanup(store VecStore) *vecCleanup {
	ctx, cancel := context.WithCancel(context.Background())
	c := &vecCleanup{cancel: cancel}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(vecStoreCleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := store.DeleteExpired(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					slog.Warn("vecstore: delete expired", "err", err)
				}
			case <-ctx.Done():
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
	c.cancel()
	c.wg.Wait()
}
