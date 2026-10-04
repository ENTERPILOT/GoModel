package providers

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// CredentialSecretEntity is the SecretKey.Entity of dashboard-managed provider
// credentials and the first segment of their secret field paths, for example
// "provider_credentials.openai.api_keys[0]" (ADR-0014 §4).
const CredentialSecretEntity = "provider_credentials"

// credentialSecretField is one field of a credential row that accepts a secret
// reference: API keys, the service-account JSON forms, and the proxy URL.
type credentialSecretField struct {
	name  string // within the entity: "api_keys[0]", "proxy_url"
	value *string
}

// credentialSecretFields returns the secret fields of cred, pointing into it.
// The caller owns cred, APIKeys included.
func credentialSecretFields(cred *ManagedProviderCredential) []credentialSecretField {
	fields := make([]credentialSecretField, 0, len(cred.APIKeys)+3)
	for i := range cred.APIKeys {
		fields = append(fields, credentialSecretField{name: CredentialFieldAPIKeys + "[" + strconv.Itoa(i) + "]", value: &cred.APIKeys[i]})
	}
	return append(fields,
		credentialSecretField{name: CredentialFieldServiceAccountJSON, value: &cred.ServiceAccountJSON},
		credentialSecretField{name: CredentialFieldServiceAccountJSONBase64, value: &cred.ServiceAccountJSONBase64},
		credentialSecretField{name: CredentialFieldProxyURL, value: &cred.ProxyURL},
	)
}

// credentialSecretValues lists the non-empty secret values of cred, for
// releasing writer-owned references it no longer holds.
func credentialSecretValues(cred *ManagedProviderCredential) []string {
	if cred == nil {
		return nil
	}
	clone := cloneCredential(*cred)
	var values []string
	for _, field := range credentialSecretFields(&clone) {
		if *field.value != "" {
			values = append(values, *field.value)
		}
	}
	return values
}

func cloneCredential(cred ManagedProviderCredential) ManagedProviderCredential {
	cred.APIKeys = slices.Clone(cred.APIKeys)
	return cred
}

func credentialSecretEntity(name string) string {
	return CredentialSecretEntity + "." + name
}

// resolveCredential returns a copy of cred with every secret reference
// resolved, and the resolution to record once the provider is installed. A
// reference that does not resolve is a field-scoped error naming the field
// and scheme, never the value.
func (s *CredentialsService) resolveCredential(ctx context.Context, cred ManagedProviderCredential) (ManagedProviderCredential, *config.ResolvedEntity, error) {
	resolved := cloneCredential(cred)
	entity := credentialSecretEntity(cred.Name)
	fields := credentialSecretFields(&resolved)
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if *field.value != "" {
			values[entity+"."+field.name] = *field.value
		}
	}
	result, err := s.secrets.ResolveEntity(ctx, entity, values)
	if err != nil {
		return ManagedProviderCredential{}, nil, credentialSecretError(entity, err)
	}
	for _, field := range fields {
		if *field.value != "" {
			*field.value = result.Value(entity + "." + field.name)
		}
	}
	return resolved, result, nil
}

// credentialSecretError turns a resolution failure into a 400 against the
// credential field holding the reference.
func credentialSecretError(entity string, err error) error {
	secretErr, ok := errors.AsType[*config.SecretError](err)
	if !ok {
		return err
	}
	field := strings.TrimPrefix(secretErr.Field, entity+".")
	if base, _, indexed := strings.Cut(field, "["); indexed {
		field = base
	}
	return &CredentialFieldError{Field: field, Message: err.Error(), Err: err}
}

// storeCredentialSecrets writes every literal secret of cred through the
// generation's SecretWriter, when one is registered, replacing it with the
// returned reference. It returns the references it created, which the caller
// releases if the save then fails. If a write fails, the references already
// created are released, except those in keep: the values of the row still
// stored, which a writer that reuses a reference may have returned again.
func (s *CredentialsService) storeCredentialSecrets(ctx context.Context, cred *ManagedProviderCredential, keep []string) ([]string, error) {
	cred.APIKeys = slices.Clone(cred.APIKeys)
	var written []string
	for _, field := range credentialSecretFields(cred) {
		key := config.SecretKey{Entity: CredentialSecretEntity, ID: cred.Name, Field: field.name}
		stored, err := s.secrets.StoreSecret(ctx, key, *field.value)
		if err != nil {
			s.releaseSecrets(ctx, cred.Name, written, keep)
			return nil, err
		}
		if stored != *field.value {
			written = append(written, stored)
			*field.value = stored
		}
	}
	return written, nil
}

// releaseSecrets deletes the writer-owned references of previous that current
// no longer holds. It is best effort: the change it follows is committed.
func (s *CredentialsService) releaseSecrets(ctx context.Context, name string, previous, current []string) {
	if err := s.secrets.ReleaseSecrets(ctx, previous, current); err != nil {
		slog.Warn("failed to delete secrets of a provider credential from the secret store", "provider", name, "error", err)
	}
}
