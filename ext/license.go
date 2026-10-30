package ext

import "time"

// License describes the commercial license a distribution runs under, for
// display only: the dashboard shows Badge after the GoModel name and counts
// down the days of a trial, or of a license close to its end. Core enforces
// nothing here and decides none of the policy: validating the license, and
// when its end is worth pointing out, stay with the distribution.
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
	// NoticeWindow is how long before ExpiresAt the dashboard starts counting
	// down a license that is not a trial. Zero counts it down only once the
	// term has ended.
	NoticeWindow time.Duration
	// WarningWindow is how long before ExpiresAt the countdown switches to
	// the warning color. Zero never shows the warning color before the end.
	WarningWindow time.Duration
}

// SetLicense records the license the dashboard shows. Setting a License with
// an empty Badge clears it.
//
// Core reads the license once, when the gateway starts, like the rest of the
// registry: call SetLicense before run.Run or app.New. A later call has no
// effect on a running gateway, including after a configuration reload.
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
