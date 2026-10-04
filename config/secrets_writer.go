package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// SecretKey names one secret field of a dashboard-managed entity, for a
// SecretWriter to derive where it stores the value. None of it is secret.
type SecretKey struct {
	// Entity is the kind of entity: "provider_credentials", "mcp_servers",
	// or "guardrail_definitions".
	Entity string
	// ID is the entity's name.
	ID string
	// Field is the field within the entity, for example "api_keys[0]",
	// "headers.Authorization", or "config.api_key".
	Field string
}

// SecretWriter stores secrets entered through the admin API in an external
// secret manager, so only a reference to them is persisted. An extension
// registers one with Secrets.SetWriter; core ships none.
//
// Implementations must be safe for concurrent use and must never include a
// secret value in an error.
type SecretWriter interface {
	// WriteSecret stores value as a new secret and returns the
	// ${scheme:reference} that resolves to it. It must not change what an
	// existing reference resolves to: a save can still fail after the write,
	// and the stored entity then keeps using its old reference. Return a
	// distinct reference per write, for example one pinned to the version
	// written. Core never deletes a reference the stored entity still holds.
	WriteSecret(ctx context.Context, key SecretKey, value string) (reference string, err error)
	// DeleteSecret removes a secret WriteSecret created.
	DeleteSecret(ctx context.Context, reference string) error
	// OwnsReference reports whether reference is one WriteSecret created,
	// so core never deletes a secret an operator referenced by hand.
	OwnsReference(reference string) bool
}

// SetWriter registers the writer literal secrets saved through the admin API
// are stored with. Like Register, it applies to one configuration generation.
// A nil w removes the writer.
func (s *Secrets) SetWriter(w SecretWriter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer = w
}

func (s *Secrets) secretWriter() SecretWriter {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writer
}

// ErrSecretNotHeld reports a writer-owned reference saved to an entity whose
// stored row does not hold it. It was read from a row that has since been
// replaced or deleted, or it belongs to another entity: either way its secret
// may already be deleted, or be deleted with that other entity.
var ErrSecretNotHeld = errors.New("refers to a stored secret this entity no longer holds; reload it and save again")

// StoreSecret returns what to persist for a secret field saved through the
// admin API. With a writer registered, a non-empty literal value is written
// through it and the reference it returns is persisted instead. A value that
// is empty or already holds a reference is returned unchanged, and so is
// every value when no writer is registered.
//
// held lists the values of the entity as currently stored. A reference the
// writer owns that held does not hold is rejected with a *SecretError
// wrapping ErrSecretNotHeld, so a save never persists a reference whose
// secret was released.
func (s *Secrets) StoreSecret(ctx context.Context, key SecretKey, value string, held []string) (string, error) {
	w := s.secretWriter()
	if w == nil || value == "" {
		return value, nil
	}
	if HasSecretReference(value) {
		if w.OwnsReference(value) && !slices.Contains(held, value) {
			return "", &SecretError{Field: key.Entity + "." + key.ID + "." + key.Field, Scheme: referenceScheme(value), Err: ErrSecretNotHeld}
		}
		return value, nil
	}
	reference, err := w.WriteSecret(ctx, key, value)
	if err != nil {
		return "", fmt.Errorf("%s.%s: write secret: %w", key.Entity, key.Field, err)
	}
	if !HasSecretReference(reference) {
		// Persisting it would store whatever the writer returned in place of
		// the secret, possibly the secret itself.
		return "", fmt.Errorf("%s.%s: secret writer did not return a ${scheme:reference}", key.Entity, key.Field)
	}
	return reference, nil
}

// ReleaseSecrets deletes, through the registered writer, every value of
// previous that the writer owns and current no longer holds: the secrets of
// a deleted entity, or of a field whose reference was replaced. Call it after
// the change is committed. Every failure is returned, joined; none of them
// undoes the change.
func (s *Secrets) ReleaseSecrets(ctx context.Context, previous, current []string) error {
	w := s.secretWriter()
	if w == nil {
		return nil
	}
	keep := make(map[string]struct{}, len(current))
	for _, value := range current {
		keep[value] = struct{}{}
	}
	var errs []error
	released := make(map[string]struct{}, len(previous))
	for _, value := range previous {
		if _, kept := keep[value]; kept || !w.OwnsReference(value) {
			continue
		}
		if _, done := released[value]; done {
			continue
		}
		released[value] = struct{}{}
		if err := w.DeleteSecret(ctx, value); err != nil {
			errs = append(errs, fmt.Errorf("delete secret %s: %w", value, err))
		}
	}
	return errors.Join(errs...)
}

// referenceScheme returns the scheme of value, a single ${scheme:reference},
// or "" when it is not one.
func referenceScheme(value string) string {
	inner, ok := strings.CutPrefix(value, "${")
	if !ok {
		return ""
	}
	scheme, _, _ := parseSecretReference(strings.TrimSuffix(inner, "}"))
	return scheme
}
