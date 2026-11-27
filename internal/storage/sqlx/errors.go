package sqlx

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation is the SQLSTATE of a row that repeats a unique key.
const uniqueViolation = "23505"

// IsUniqueViolation reports whether err is an engine's refusal of a row that
// repeats a unique key: PostgreSQL's SQLSTATE 23505, and SQLite's "UNIQUE
// constraint failed".
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == uniqueViolation
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
