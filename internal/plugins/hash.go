package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// RuleDescriptor describes one chain member for hashing. Mode carries the
// instance fail mode and Content its config digest.
type RuleDescriptor struct {
	Name    string
	Type    string
	Order   int
	Mode    string
	Content string
}

// ComputeChainHash computes a deterministic hash for a set of chain members.
// Each member is represented as "name:type:order:mode:content_hash"; the seeds
// are sorted, joined and passed through SHA-256. The result is carried on the
// request context (core.WithGuardrailsHash) and consumed by the response cache
// as an opaque key component, so configuration changes invalidate cached
// completions. Empty is reserved for "no chain".
func ComputeChainHash(rules []RuleDescriptor) string {
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

func xxhashString(s string) uint64 {
	return xxhash.Sum64String(s)
}
