package cache

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// installRedisLogger routes go-redis's package-level internal logger through
// slog, so its connection-pool and pubsub diagnostics honor the application's
// configured log format and level instead of writing raw lines straight to
// stderr. go-redis exposes only one process-global logger, so this is set once.
var installRedisLogger = sync.OnceFunc(func() {
	redis.SetLogger(slogRedisLogger{})
})

// slogRedisLogger adapts go-redis's internal.Logging interface to slog. go-redis
// uses this logger for non-fatal operational diagnostics, so messages are
// emitted at warn level; the higher-level operation that triggered them is
// logged separately by the caller.
type slogRedisLogger struct{}

func (slogRedisLogger) Printf(ctx context.Context, format string, v ...any) {
	slog.WarnContext(ctx, fmt.Sprintf(format, v...))
}
