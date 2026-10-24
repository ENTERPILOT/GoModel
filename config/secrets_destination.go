package config

import (
	"context"
	"errors"
	"strings"
)

// ErrSecretDestinationRestricted reports a save, in a context from
// RestrictSecretReferences, that changes where an entity holding a secret
// reference sends it.
var ErrSecretDestinationRestricted = errors.New("only the master key can change where an entity holding a secret reference sends it")

// SecretDestination is a field of an admin-saved entity that decides where the
// entity sends its secrets, such as an upstream URL, with its stored and saved
// values.
type SecretDestination struct {
	// Field is the field within the entity, for example "url" or "base_url".
	Field         string
	Stored, Saved string
}

// CheckSecretDestinations returns, in a context from RestrictSecretReferences,
// a *SecretError wrapping ErrSecretDestinationRestricted naming the first
// destination that changes while values, the entity's secret values as saved,
// hold a secret reference. Repointing an entity at another host sends it what
// the reference resolves to, which reads the reference as adding one would.
// Literal secrets are the caller's own, so they do not count. Callers pass no
// destinations for a new entity: StoreSecret already refuses its references.
func CheckSecretDestinations(ctx context.Context, entity, id string, destinations []SecretDestination, values []string) error {
	if !secretReferencesRestricted(ctx) || len(secretReferenceSet(values)) == 0 {
		return nil
	}
	for _, destination := range destinations {
		if strings.TrimSpace(destination.Stored) != strings.TrimSpace(destination.Saved) {
			return &SecretError{Field: entity + "." + id + "." + destination.Field, Err: ErrSecretDestinationRestricted}
		}
	}
	return nil
}
