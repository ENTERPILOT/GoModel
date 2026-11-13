package sqlx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateIndexPattern(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		index     string
		table     string
	}{
		{name: "plain", statement: `CREATE INDEX IF NOT EXISTS idx_usage_model ON usage(model)`, index: "idx_usage_model", table: "usage"},
		{name: "unique across lines", statement: "\n\tCREATE UNIQUE INDEX IF NOT EXISTS idx_scope\n\t\tON workflow_versions (scope_key) WHERE active", index: "idx_scope", table: "workflow_versions"},
		{name: "access method", statement: `CREATE INDEX IF NOT EXISTS idx_gin ON usage USING GIN (raw_data)`, index: "idx_gin", table: "usage"},
		{name: "quoted", statement: `CREATE INDEX IF NOT EXISTS "Cache_idx" ON "my ""cache""" (expires_at)`, index: `"Cache_idx"`, table: `"my ""cache"""`},
		{name: "schema qualified", statement: `create index if not exists idx_a on public.items(a)`, index: "idx_a", table: "public.items"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := createIndexPattern.FindStringSubmatch(tt.statement)
			require.NotNil(t, match)
			assert.Equal(t, tt.index, match[1])
			assert.Equal(t, tt.table, match[2])
		})
	}

	// Without IF NOT EXISTS the statement must run: a skipped duplicate
	// would hide the error the caller asked for.
	for _, statement := range []string{
		`CREATE INDEX idx_usage_model ON usage(model)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_model ON usage(model)`,
		`DROP INDEX IF EXISTS idx_usage_model`,
		`CREATE TABLE IF NOT EXISTS usage (id TEXT)`,
	} {
		assert.Nil(t, createIndexPattern.FindStringSubmatch(statement), statement)
	}
}

func TestIdentifierName(t *testing.T) {
	assert.Equal(t, "idx_usage_model", identifierName("IDX_Usage_Model"))
	assert.Equal(t, "Cache_idx", identifierName(`"Cache_idx"`))
	assert.Equal(t, `my "cache"`, identifierName(`"my ""cache"""`))
}
