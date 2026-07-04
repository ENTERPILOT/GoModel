package guardrails

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// RuleDescriptor describes a single active guardrail rule for hashing.
type RuleDescriptor struct {
	Name    string
	Type    string
	Order   int
	Mode    string
	Content string
}

// ComputeGuardrailsHash computes the guardrails_hash for a set of rule identifiers.
// Each rule is represented as "name:type:order:mode:content_hash". The combined seed is
// sorted for stability, then passed through SHA-256.
// Uses xxhash64 per-component and SHA-256 for the final hash to balance speed and
// collision resistance.
//
// The hash is carried on the request context (core.WithGuardrailsHash) and
// consumed by the response cache as an opaque key component, so guardrail
// configuration changes invalidate cached completions.
func ComputeGuardrailsHash(rules []RuleDescriptor) string {
	if len(rules) == 0 {
		return ""
	}
	seeds := make([]string, len(rules))
	for i, r := range rules {
		contentXX := xxhash.Sum64String(r.Content)
		seeds[i] = fmt.Sprintf("%s:%s:%d:%s:%016x", r.Name, r.Type, r.Order, r.Mode, contentXX)
	}
	sort.Strings(seeds)
	combined := strings.Join(seeds, "|")
	h := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(h[:])
}
