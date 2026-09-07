package pluginapi

// Exchange is the unified view of one request/response pair that every hook
// receives. Which fields are set depends on the phase: Prompt is nil for
// non-inference routes, Response is nil until the response phase, and Stream
// is nil for non-streaming requests.
type Exchange struct {
	// Meta is read-only identity and routing information.
	Meta Meta
	// Prompt is the unified request. Edit it through its methods.
	Prompt *Prompt
	// Response is the unified response. Edit it through its methods.
	Response *Completion
	// Stream is the accumulated stream state for streaming requests.
	Stream *StreamState
	// Headers carries inbound request headers and outbound response headers.
	Headers *Headers
	// Values is a per-request bag for passing state between a plugin's own
	// hooks (for example from OnPrompt to OnResponse). Keys should be
	// prefixed with the plugin name to avoid collisions.
	Values Values
}

// Values is a per-request key/value bag. The host allocates it before the
// first hook runs.
type Values map[string]any

// Get returns the value stored under key and whether it was present. It is
// safe to call on a nil map.
func (v Values) Get(key string) (any, bool) {
	if v == nil {
		return nil, false
	}
	value, ok := v[key]
	return value, ok
}

// Set stores value under key. The map must be non-nil; the host always
// allocates Exchange.Values.
func (v Values) Set(key string, value any) {
	v[key] = value
}
