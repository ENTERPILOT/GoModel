//go:build cgo

package responsecache

import (
	"sync"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

var sqliteVecAutoOnce sync.Once

func newSQLiteVecStore(path string) (*sqliteVecStore, error) {
	sqliteVecAutoOnce.Do(sqlitevec.Auto)
	db, err := openSQLiteVecDB(path)
	if err != nil {
		return nil, err
	}
	s := &sqliteVecStore{db: db, stopCh: make(chan struct{})}
	s.wg.Add(1)
	go s.cleanupLoop()
	return s, nil
}
