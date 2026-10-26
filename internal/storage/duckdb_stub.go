//go:build !duckdb

package storage

import "fmt"

// NewDuckDB reports that this binary was built without DuckDB, which needs
// cgo and is therefore not part of the default static build.
func NewDuckDB(DuckDBConfig) (DuckDBStorage, error) {
	return nil, fmt.Errorf("storage type duckdb is not available: rebuild with CGO_ENABLED=1 -tags duckdb")
}
