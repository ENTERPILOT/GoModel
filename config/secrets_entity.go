package config

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// ResolvedEntity holds the resolved secret fields of one dashboard-managed
// entity (a provider credential, an MCP server, a guardrail), as returned by
// ResolveEntity. Nothing is recorded for rotation until Record is called.
type ResolvedEntity struct {
	secrets *Secrets
	entity  string
	values  map[string]string
	uses    map[string]secretUse
}

// ResolveEntity resolves the secret fields of one dashboard-managed entity as
// a unit. entity identifies it ("provider_credentials.openai"); fields maps
// each field path ("provider_credentials.openai.api_keys[0]") to its stored
// value. Resolvers see the field path, as with ResolveFields.
//
// The first field that fails returns its *SecretError, naming the field and
// scheme, and nothing is recorded. On success, call Record once the entity is
// installed so rotation tracks exactly the values it runs on.
func (s *Secrets) ResolveEntity(ctx context.Context, entity string, fields map[string]string) (*ResolvedEntity, error) {
	r := &ResolvedEntity{
		secrets: s,
		entity:  entity,
		values:  make(map[string]string, len(fields)),
		uses:    make(map[string]secretUse),
	}
	// Sorted, so the reported failure is the same on every attempt.
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		value := fields[field]
		resolved, referenced, err := s.resolveValue(ctx, field, value)
		if err != nil {
			return nil, err
		}
		r.values[field] = resolved
		if referenced {
			r.uses[field] = secretUse{original: value, fingerprint: fingerprintSecret(resolved)}
		}
	}
	return r, nil
}

// Value returns the resolved value of field, or "" when it was not resolved.
func (r *ResolvedEntity) Value(field string) string {
	if r == nil {
		return ""
	}
	return r.values[field]
}

// Record makes these fields the ones rotation compares against for the
// entity, replacing whatever it recorded before: a field that no longer holds
// a reference stops being watched. Safe on a nil receiver.
func (r *ResolvedEntity) Record() {
	if r == nil || r.secrets == nil || r.entity == "" {
		return
	}
	rot := &r.secrets.rotation
	rot.mu.Lock()
	defer rot.mu.Unlock()
	rot.forgetLocked(r.entity)
	if len(r.uses) == 0 {
		return
	}
	if rot.uses == nil {
		rot.uses = make(map[string]secretUse)
	}
	if rot.owned == nil {
		rot.owned = make(map[string][]string)
	}
	fields := make([]string, 0, len(r.uses))
	for field, use := range r.uses {
		rot.uses[field] = use
		fields = append(fields, field)
	}
	rot.owned[r.entity] = fields
}

// ForgetEntity stops rotation from watching the fields entity recorded, for
// an entity that was deleted or is no longer installed.
func (s *Secrets) ForgetEntity(entity string) {
	if s == nil {
		return
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	s.rotation.forgetLocked(entity)
}

// EntityOwnsAny reports whether entity recorded any of fields for rotation
// (see Record). It matches exact field paths, so the entity "pii" never owns
// a field of "pii.team" although its paths share the prefix
// "guardrail_definitions.pii.".
func (s *Secrets) EntityOwnsAny(entity string, fields []string) bool {
	if s == nil || len(fields) == 0 {
		return false
	}
	s.rotation.mu.Lock()
	defer s.rotation.mu.Unlock()
	return slices.ContainsFunc(s.rotation.owned[entity], func(owned string) bool {
		return slices.Contains(fields, owned)
	})
}

func (rot *rotation) forgetLocked(entity string) {
	for _, field := range rot.owned[entity] {
		delete(rot.uses, field)
	}
	delete(rot.owned, entity)
}

// OnlySecretReferences reports whether value is made of secret references
// alone, one or more, with no literal text around or between them. The admin
// API shows such a value as stored; a value that mixes literal text with a
// reference may carry a literal secret and is masked like one.
func OnlySecretReferences(value string) bool {
	if value == "" {
		return false
	}
	for rest := value; rest != ""; {
		if !strings.HasPrefix(rest, "${") {
			return false
		}
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return false
		}
		if _, _, ok := parseSecretReference(rest[2:end]); !ok {
			return false
		}
		rest = rest[end+1:]
	}
	return true
}
