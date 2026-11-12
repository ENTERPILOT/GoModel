package usagemetrics

import (
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// UnknownAuthKey is reported for a managed key whose name cannot be resolved,
// for example one deleted while its request was still streaming.
const UnknownAuthKey = "{unknown}"

// Caller is who made a request, as metric dimension values. A dimension that
// is switched off, or that has no value for the request, is empty.
type Caller struct {
	// UserPath is the request's user path, cut to the configured depth.
	UserPath string
	// AuthKey is the name of the managed API key; empty for the master key
	// and for unauthenticated traffic.
	AuthKey string
	// RequestLabels is the request's labels, sorted and comma-separated;
	// see joinLabels for the encoding.
	RequestLabels string
}

// Callers resolves the opt-in caller dimensions. A nil *Callers has every
// dimension switched off.
type Callers struct {
	userPathDepth int // 0 when the user path dimension is off
	authKeyName   func(id string) string
	requestLabels bool
	maxValues     int
}

// NewCallers builds the resolver for the configured dimensions, or returns
// nil when all of them are off. authKeyName maps a managed key ID to its
// name; without it the auth key dimension stays off.
func NewCallers(cfg config.MetricsCallerDimensionsConfig, authKeyName func(id string) string) *Callers {
	c := &Callers{requestLabels: cfg.RequestLabels, maxValues: cfg.MaxValues}
	if c.maxValues < 1 {
		c.maxValues = config.DefaultMetricsCallerMaxValues
	}
	if cfg.UserPath {
		c.userPathDepth = cfg.UserPathDepth
		if c.userPathDepth < 1 {
			c.userPathDepth = config.DefaultMetricsUserPathDepth
		}
	}
	if cfg.AuthKey {
		c.authKeyName = authKeyName
	}
	if c.userPathDepth == 0 && c.authKeyName == nil && !c.requestLabels {
		return nil
	}
	return c
}

// Resolve turns a request's identity into bounded dimension values.
func (c *Callers) Resolve(userPath, authKeyID string, labels []string) Caller {
	if c == nil {
		return Caller{}
	}
	var caller Caller
	if c.userPathDepth > 0 {
		caller.UserPath = admitted.userPaths.admit(truncateUserPath(userPath, c.userPathDepth), OtherUserPath, c.maxValues)
	}
	if c.authKeyName != nil {
		if authKeyID = strings.TrimSpace(authKeyID); authKeyID != "" {
			if name := strings.TrimSpace(c.authKeyName(authKeyID)); name != "" {
				caller.AuthKey = admitted.authKeys.admit(name, OtherValue, c.maxValues)
			} else {
				// One fixed value, so it takes no slot.
				caller.AuthKey = UnknownAuthKey
			}
		}
	}
	if c.requestLabels {
		caller.RequestLabels = admitted.requestLabels.admit(joinLabels(labels), OtherValue, c.maxValues)
	}
	return caller
}

// truncateUserPath keeps the first depth segments of a user path. A missing
// or invalid path is the root, as in usage tracking.
func truncateUserPath(userPath string, depth int) string {
	normalized, err := core.NormalizeUserPath(userPath)
	if err != nil || normalized == "" || normalized == "/" {
		return "/"
	}
	segments := strings.Split(normalized[1:], "/")
	if len(segments) > depth {
		segments = segments[:depth]
	}
	return "/" + strings.Join(segments, "/")
}

// labelEscaper percent-encodes the separator inside a label, and the percent
// sign itself, so the joined value can be split back into its labels.
var labelEscaper = strings.NewReplacer("%", "%25", ",", "%2C")

// joinLabels renders a label set as one value that does not depend on the
// order the labels arrived in: the labels sorted and joined by commas. A
// comma inside a label is written %2C (and a percent sign %25), so a comma
// in the value always separates two labels and distinct sets never share a
// value: ["a,b"] is "a%2Cb", ["a", "b"] is "a,b".
func joinLabels(labels []string) string {
	labels = core.MergeLabels(labels)
	slices.Sort(labels)
	for i, label := range labels {
		labels[i] = labelEscaper.Replace(label)
	}
	return strings.Join(labels, ",")
}
