package sqlx

import "time"

// SetLockWait shortens how long db's schema changes wait for a table lock, so
// a test of a blocked change does not wait out the production bounds.
func SetLockWait(db DB, timeout, pause time.Duration, attempts int) {
	db.(*postgresDB).lockWait = lockWait{timeout: timeout, pause: pause, attempts: attempts}
}
