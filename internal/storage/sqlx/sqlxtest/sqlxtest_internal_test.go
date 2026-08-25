package sqlxtest

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransientCatalogRace(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "tuple concurrently updated", err: &pgconn.PgError{Code: "XX000", Message: "tuple concurrently updated"}, want: true},
		{name: "deadlock", err: &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}, want: true},
		{name: "other internal error", err: &pgconn.PgError{Code: "XX000", Message: "something else broke"}, want: false},
		{name: "missing schema", err: &pgconn.PgError{Code: "3F000", Message: "schema does not exist"}, want: false},
		{name: "wrapped pg error", err: errors.Join(errors.New("exec"), &pgconn.PgError{Code: "XX000", Message: "tuple concurrently updated"}), want: true},
		{name: "non-pg error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientCatalogRace(tt.err); got != tt.want {
				t.Fatalf("isTransientCatalogRace(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
