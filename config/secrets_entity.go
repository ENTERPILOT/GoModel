package config

import (
	"context"
	"maps"
	"slices"
)

// HasSecretReference reports whether value contains at least one
// ${scheme:reference} secret reference. An escaped $${...} is not one.
//
// The admin API uses it to tell a reference, which names where a secret lives
// and is shown as is, from a literal secret, which it masks.
func HasSecretReference(value string) bool {
	return hasSecretReference(value)
}

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
// value. Resolvers see the field path, as with ResolveField.
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

func (rot *rotation) forgetLocked(entity string) {
	for _, field := range rot.owned[entity] {
		delete(rot.uses, field)
	}
	delete(rot.owned, entity)
}
