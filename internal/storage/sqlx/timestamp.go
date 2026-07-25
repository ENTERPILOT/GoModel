package sqlx

import (
	"fmt"
	"time"
)

// Timestamp scans a TypeTimestamp column from either engine. It is the read
// side of TimestampArg: PostgreSQL hands back a time.Time, SQLite the RFC3339
// text that was written.
//
// Text it cannot parse leaves Time zero and Valid false rather than failing
// the scan. A reader returning a page of rows should not fail the whole page
// because one row holds an unreadable timestamp; callers that care report Raw.
type Timestamp struct {
	Time  time.Time
	Valid bool
	Raw   string
}

// Scan implements sql.Scanner, which both drivers honour.
func (t *Timestamp) Scan(src any) error {
	*t = Timestamp{}
	switch value := src.(type) {
	case nil:
		return nil
	case time.Time:
		// Deliberately not normalised to UTC: each driver's own zone is what
		// callers already render, and changing it would move every timestamp
		// the admin API returns.
		t.Time, t.Valid = value, true
		return nil
	case string:
		t.parseText(value)
		return nil
	case []byte:
		t.parseText(string(value))
		return nil
	default:
		return fmt.Errorf("cannot scan %T into a timestamp", src)
	}
}

// timestampLayouts are the spellings a TypeTimestamp column has held. The
// first is what TimestampArg writes; the rest are older rows and values
// written by SQLite's own date functions.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05Z",
}

func (t *Timestamp) parseText(raw string) {
	t.Raw = raw
	for _, layout := range timestampLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			t.Time, t.Valid = parsed, true
			return
		}
	}
}
