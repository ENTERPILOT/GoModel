package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
)

// RotateSecrets re-resolves the dashboard-managed credentials whose secret
// fields changed, as reported by a secret recheck, and reinstalls each one
// whose resolved values differ (ADR-0014 §5). When only a provider's API keys
// changed they are swapped into its keyring in place; any other change
// rebuilds the provider through the same install path a save uses.
//
// A credential whose references cannot be re-resolved, or that no longer
// builds, keeps running on its current values; the error says which.
func (s *CredentialsService) RotateSecrets(ctx context.Context, fields []string) error {
	rebuilt, err := s.rotateCredentials(ctx, fields)
	// Outside applyMu, as for a save: the refresh calls every provider.
	if rebuilt {
		if err := s.registry.Refresh(ctx); err != nil {
			slog.Warn("provider rebuilt for a rotated secret but the model catalog refresh failed", "error", err)
		}
	}
	return err
}

// rotateCredentials applies the current secret values to every installed
// credential owning any of fields, reporting whether one was rebuilt.
func (s *CredentialsService) rotateCredentials(ctx context.Context, fields []string) (bool, error) {
	var errs []error
	rebuilt := false
	for _, name := range s.rotatedCredentials(fields) {
		changed, err := s.rotateCredential(ctx, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("provider credential %q: %w", name, err))
			continue
		}
		rebuilt = rebuilt || changed
	}
	return rebuilt, errors.Join(errs...)
}

// rotatedCredentials lists the installed credentials owning any of fields.
func (s *CredentialsService) rotatedCredentials(fields []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var names []string
	for _, name := range slices.Sorted(maps.Keys(s.configs)) {
		// Exact fields, not a prefix: names may contain dots, and
		// "provider_credentials.openai." prefixes the fields of "openai.eu".
		if s.secrets.EntityOwnsAny(credentialSecretEntity(name), fields) {
			names = append(names, name)
		}
	}
	return names
}

// rotateCredential applies the current secret values to one installed
// credential, reporting whether the provider was rebuilt. Like a save, it
// resolves before taking applyMu. If the credential was installed meanwhile,
// by a save or an earlier rotation, it resolves again rather than replace
// newer values; a credential deleted or disabled meanwhile is left alone.
func (s *CredentialsService) rotateCredential(ctx context.Context, name string) (bool, error) {
	for {
		rebuilt, retry, err := s.rotateCredentialOnce(ctx, name)
		if !retry {
			return rebuilt, err
		}
	}
}

// rotateCredentialOnce is one attempt of rotateCredential. It reports retry,
// changing nothing, when name was installed, removed, or swapped while it
// resolved.
func (s *CredentialsService) rotateCredentialOnce(ctx context.Context, name string) (rebuilt, retry bool, err error) {
	revision := s.revision(name)
	row, err := s.storedCredential(ctx, name)
	if err != nil || row == nil || !row.Enabled || s.IsManaged(name) {
		return false, false, err
	}
	resolved, secrets, err := s.resolveCredential(ctx, *row)
	if err != nil {
		return false, false, err
	}
	next, err := s.providerConfig(*row, resolved)
	if err != nil {
		return false, false, err
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.revision(name) != revision {
		return false, true, nil
	}

	s.mu.Lock()
	current, keys := s.configs[name], s.keyrings[name]
	if sameExceptKeys(current, next) && keys.Replace(next.APIKeys...) {
		s.configs[name] = next
		s.revisions[name]++
		s.mu.Unlock()
		secrets.Record()
		if !slices.Equal(current.APIKeys, next.APIKeys) {
			slog.Info("rotated provider API keys in place", "provider", name)
		}
		return false, false, nil
	}
	s.mu.Unlock()

	built, err := s.buildProvider(*row, resolved)
	if err != nil {
		return false, false, err
	}
	built.secrets = secrets
	s.install(name, built)
	slog.Info("rebuilt provider for a rotated secret", "provider", name)
	return true, false, nil
}

// sameExceptKeys reports whether two configurations of one provider differ in
// nothing but their API keys, and both have at least one.
func sameExceptKeys(current, next ProviderConfig) bool {
	if len(current.APIKeys) == 0 || len(next.APIKeys) == 0 {
		return false
	}
	current.APIKey, current.APIKeys = "", nil
	next.APIKey, next.APIKeys = "", nil
	return reflect.DeepEqual(current, next)
}
