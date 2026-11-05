package testconventions

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// TestCIRunsEveryStoreBackend fails a CI run that has lost its PostgreSQL or
// MongoDB test database. The store suites skip a backend whose variable is
// unset, so a workflow edit that drops one would leave CI green while every
// PostgreSQL and MongoDB subtest stops running. GitHub Actions sets CI itself,
// so the guard cannot be switched off by the same edit. Locally, where CI is
// unset, it skips: a developer without the databases still gets a green run.
func TestCIRunsEveryStoreBackend(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("enforced only in CI (CI is set)")
	}
	for _, name := range []string{sqlxtest.PostgresURLEnv, mongotest.DSNEnv} {
		assert.NotEmpty(t, strings.TrimSpace(os.Getenv(name)),
			"%s is unset in CI, so its store suites would silently skip; start the database "+
				"(tools/ci/postgres.sh, tools/ci/mongo-replset.sh) and export it, or unset CI "+
				"when running the suite outside CI", name)
	}
}
