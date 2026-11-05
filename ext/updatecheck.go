package ext

// UpdateCheckLicense is what a licensed distribution adds to the daily update
// check, so the release host can count license holders rather than only
// deployments: one organization often runs several gateways, each with its
// own database and therefore its own install id.
//
// Core sends both values only when they have exactly the shapes documented
// below and drops them otherwise, so an extension cannot use the check to
// carry anything else.
type UpdateCheckLicense struct {
	// Kind is the license the gateway runs on: "paid", "trial", "beta" (a
	// build-time license every copy of a beta build shares) or "none".
	Kind string
	// Account identifies the license holder: 32 lowercase hex characters,
	// the same for every deployment one holder runs, or empty. It must be a
	// keyed digest that only the license issuer can match to a holder, never
	// the holder's name.
	Account string
}

// SetUpdateCheckLicense records the license the update check reports. A later
// call replaces an earlier one.
func (r *Registry) SetUpdateCheckLicense(license UpdateCheckLicense) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCheckLicense = license
}

// UpdateCheckLicense returns the license the update check reports; the zero
// value when none was set or the registry is nil.
func (r *Registry) UpdateCheckLicense() UpdateCheckLicense {
	if r == nil {
		return UpdateCheckLicense{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateCheckLicense
}
