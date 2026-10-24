package ext

import "time"

// License describes the commercial license a distribution runs under, for
// display only: the dashboard shows Badge after the GoModel name and counts
// down the days of a trial, or of a license close to its end. Core enforces
// nothing here; validating the license stays with the distribution.
type License struct {
	// Badge is the short label shown after the GoModel name, e.g. "PRO".
	// An empty Badge means no license is shown.
	Badge string
	// Trial marks an evaluation license, whose days are always counted down.
	Trial bool
	// ExpiresAt is when the license term ends. Zero shows no countdown.
	ExpiresAt time.Time
	// ValidUntil is when the license stops working, after any grace period.
	// Zero, or a time before ExpiresAt, means there is no grace period.
	ValidUntil time.Time
}

// SetLicense records the license the dashboard shows. Setting a License with
// an empty Badge clears it.
func (r *Registry) SetLicense(license License) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.license = license
}

// License returns the license recorded by SetLicense, and whether there is
// one to show.
func (r *Registry) License() (License, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.license, r.license.Badge != ""
}
