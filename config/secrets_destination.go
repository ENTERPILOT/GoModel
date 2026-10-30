package config

import (
	"context"
	"errors"
	"strings"
)

// ErrSecretDestinationRestricted reports a save, in a context from
// RestrictSecretReferences, that changes where an entity sends a secret the
// caller did not supply: one it holds as a reference, or one kept from the
// stored row behind a redaction mask.
var ErrSecretDestinationRestricted = errors.New("only the master key can change where an entity sends a secret it already holds; supply the secret again to repoint it")

// SecretDestination is a field of an admin-saved entity that decides where the
// entity sends its secrets, such as an upstream URL, with its stored and saved
// values.
type SecretDestination struct {
	// Field is the field within the entity, for example "url" or "base_url".
	Field         string
	Stored, Saved string
}

type keptSecretsKey struct{}

// WithKeptSecrets returns a context recording that values, secret values of
// the entity being saved, were not supplied by the caller but resolved from
// the stored row, which is how an admin API resolves the redaction mask the
// dashboard echoes back. CheckSecretDestinations treats them like references:
// the caller cannot read them, so it may not repoint the entity that sends
// them.
func WithKeptSecrets(ctx context.Context, values ...string) context.Context {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			kept = append(kept, value)
		}
	}
	if len(kept) == 0 {
		return ctx
	}
	return context.WithValue(ctx, keptSecretsKey{}, append(keptSecrets(ctx), kept...))
}

func keptSecrets(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	kept, _ := ctx.Value(keptSecretsKey{}).([]string)
	return kept
}

// CheckSecretDestinations returns, in a context from RestrictSecretReferences,
// a *SecretError wrapping ErrSecretDestinationRestricted naming the first
// destination that changes while the entity keeps a secret the caller cannot
// read: values, the entity's secret values as saved, hold a secret reference
// or a value the context marks as kept (WithKeptSecrets). Repointing such an
// entity at another host sends it the secret, which reads the reference, or
// the masked literal, as plainly as fetching it would. A secret the caller
// typed in is its own, so a save that supplies every secret afresh may
// repoint. Callers pass no destinations for a new entity: StoreSecret already
// refuses its references, and it has no stored secrets to keep.
func CheckSecretDestinations(ctx context.Context, entity, id string, destinations []SecretDestination, values []string) error {
	if !secretReferencesRestricted(ctx) {
		return nil
	}
	if len(secretReferenceSet(values)) == 0 && !holdsKeptSecret(values, keptSecrets(ctx)) {
		return nil
	}
	for _, destination := range destinations {
		if strings.TrimSpace(destination.Stored) != strings.TrimSpace(destination.Saved) {
			return &SecretError{Field: entity + "." + id + "." + destination.Field, Err: ErrSecretDestinationRestricted}
		}
	}
	return nil
}

// holdsKeptSecret reports whether any saved value is one the context marks as
// kept from the stored row.
func holdsKeptSecret(values, kept []string) bool {
	if len(values) == 0 || len(kept) == 0 {
		return false
	}
	held := make(map[string]struct{}, len(kept))
	for _, value := range kept {
		held[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := held[strings.TrimSpace(value)]; ok {
			return true
		}
	}
	return false
}
