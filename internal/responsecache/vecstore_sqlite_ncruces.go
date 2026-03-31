//go:build !cgo

package responsecache

import (
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func newSQLiteVecStore(path string) (*sqliteVecStore, error) {
	db, err := openSQLiteVecDB(path)
	if err != nil {
		return nil, err
	}
	s := &sqliteVecStore{db: db, stopCh: make(chan struct{})}
	s.wg.Add(1)
	go s.cleanupLoop()
	return s, nil
}
