package sqlx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Store constructors re-apply their whole schema on every start, and several
// replicas start against one database. AddColumns already leaves out the
// columns that exist. A `CREATE INDEX IF NOT EXISTS` that changes nothing has
// the same problem: it still queues for a SHARE lock on its table first, which
// waits for every write in progress, and every later write on the table then
// queues behind it. So the index statements that would change nothing are not
// sent at all, and the schema changes that are needed stop waiting for their
// lock after lockWait rather than hold the table's traffic up behind them.

// lockWait bounds how long a schema change waits for a table lock.
type lockWait struct {
	// timeout is how long one attempt waits. It stays below PostgreSQL's
	// default deadlock_timeout of one second: a schema change and a
	// CREATE INDEX CONCURRENTLY wait on each other, and the deadlock check
	// would cancel the build, which loses minutes of work, rather than the
	// schema change, which loses nothing.
	timeout time.Duration
	// pause lets the statements that queued behind a failed attempt run
	// before the next one.
	pause    time.Duration
	attempts int
}

var defaultLockWait = lockWait{timeout: 500 * time.Millisecond, pause: time.Second, attempts: 40}

// ErrTableLocked reports a schema change that gave up waiting for its table
// lock: a long-running statement or index build held the table throughout.
var ErrTableLocked = errors.New("schema change could not lock its table")

// lockNotAvailable is the SQLSTATE of a statement cancelled by lock_timeout.
const lockNotAvailable = "55P03"

const pgIdentifier = `(?:"(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`

var createIndexPattern = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+(` +
	pgIdentifier + `)\s+ON\s+(` + pgIdentifier + `(?:\.` + pgIdentifier + `)?)`)

// changeSchema runs fn in a transaction that holds the schema advisory lock
// and gives up waiting for a table lock after lockWait.timeout, retrying the
// whole transaction when it does.
func (p *postgresDB) changeSchema(ctx context.Context, fn func(Querier) error) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = p.InTx(ctx, func(q Querier) error {
			if _, err := q.Exec(ctx, "SELECT pg_advisory_xact_lock(?)", postgresSchemaLockKey); err != nil {
				return fmt.Errorf("acquire schema lock: %w", err)
			}
			// Set only once the advisory lock is held, so that it bounds
			// the wait for a table and not the wait above: replicas queueing
			// there to apply their schema is expected.
			timeout := fmt.Sprintf("SET LOCAL lock_timeout = %d", p.lockWait.timeout.Milliseconds())
			if _, err := q.Exec(ctx, timeout); err != nil {
				return fmt.Errorf("set lock timeout: %w", err)
			}
			return fn(q)
		})
		if !isLockTimeout(err) || attempt >= p.lockWait.attempts {
			break
		}
		if attempt == 1 {
			slog.Warn("schema change is waiting for a table lock; retrying", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.lockWait.pause):
		}
	}
	if isLockTimeout(err) {
		return fmt.Errorf("%w in %d attempts; a long-running statement or index build holds it: %w",
			ErrTableLocked, p.lockWait.attempts, err)
	}
	return err
}

func isLockTimeout(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == lockNotAvailable
}

// pendingSchema drops the CREATE INDEX IF NOT EXISTS statements whose index
// already exists. A statement it cannot parse is kept, so it runs as written.
func pendingSchema(ctx context.Context, q Querier, statements []string) ([]string, error) {
	pending := make([]string, 0, len(statements))
	for _, statement := range statements {
		match := createIndexPattern.FindStringSubmatch(statement)
		if match == nil {
			pending = append(pending, statement)
			continue
		}
		// IF NOT EXISTS looks for any relation of that name in the table's
		// schema, so this does too.
		var exists bool
		err := q.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_class i
				JOIN pg_class t ON t.relnamespace = i.relnamespace
				WHERE t.oid = to_regclass(?) AND i.relname = ?
			)`, match[2], identifierName(match[1])).Scan(&exists)
		if err != nil {
			return nil, fmt.Errorf("look up index %s: %w", match[1], err)
		}
		if !exists {
			pending = append(pending, statement)
		}
	}
	return pending, nil
}

// identifierName is the name PostgreSQL stores for an identifier as written
// in SQL: a quoted one verbatim, an unquoted one folded to lower case.
func identifierName(identifier string) string {
	if strings.HasPrefix(identifier, `"`) {
		return strings.ReplaceAll(identifier[1:len(identifier)-1], `""`, `"`)
	}
	return strings.ToLower(identifier)
}
