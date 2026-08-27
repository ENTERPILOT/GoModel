package versioncheck

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// CookieName is the browser cookie holding the visit marker. Its value is
// YYYY-MM-DD-{id}: the day this browser last checked, plus a random id
// generated on the first visit. Dashboard JavaScript reads the date half to
// decide whether today's check has already happened, so the cookie is
// deliberately not HttpOnly.
const CookieName = "gomodel_version_check"

// CookieMaxAge keeps a browser's id stable for a year.
const CookieMaxAge = 31536000

// dateLength is the length of the YYYY-MM-DD prefix.
const dateLength = len("2006-01-02")

// SplitVisit separates a cookie value into its date and id halves. An empty
// or malformed value yields empty strings, which callers treat as a first
// visit.
//
// The id must be exactly the canonical UUID NewVisit generates. /version is
// unauthenticated and the id is echoed into an outbound request header and
// back into Set-Cookie, so an unvalidated one would let any caller put text of
// their choosing into both. Anything else is discarded and replaced with a
// fresh id, which is also the right answer for a cookie corrupted in transit.
func SplitVisit(value string) (date, id string) {
	value = strings.TrimSpace(value)
	if len(value) < dateLength+2 || value[dateLength] != '-' {
		return "", ""
	}
	date = value[:dateLength]
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", ""
	}
	id = value[dateLength+1:]
	if !validID(id) {
		return "", ""
	}
	return date, id
}

// validID reports whether id is a canonical lower-case hyphenated UUID.
// uuid.Parse alone is too permissive: it also accepts braced, URN and
// unhyphenated forms, so the round-trip comparison pins the exact shape
// NewVisit emits.
func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// NewVisit builds a cookie value for today, reusing id when the browser
// already has one and minting a fresh one otherwise.
//
// The day is UTC on both sides of the cookie: the dashboard reads the same
// value back, and a browser in a different timezone to the gateway would
// otherwise disagree about when a new day starts.
func NewVisit(id string, now time.Time) string {
	if id == "" {
		id = uuid.NewString()
	}
	return now.UTC().Format(time.DateOnly) + "-" + id
}

// DueToday reports whether a browser presenting this cookie value has yet to
// check in on the given day.
func DueToday(value string, now time.Time) bool {
	date, id := SplitVisit(value)
	return id == "" || date != now.UTC().Format(time.DateOnly)
}
