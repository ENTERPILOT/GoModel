package providers

import (
	"context"
	"errors"
	"slices"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// credentialSecretKind names provider credentials in sealed values' AAD.
const credentialSecretKind = "provider_credential"

// sealedCredentialStore encrypts a credential's secret fields on the way into
// the store and decrypts them on the way out, so the service and the admin
// API (which masks them) only ever see plaintext.
type sealedCredentialStore struct {
	CredentialStore
	box *encryption.Box
}

func sealCredentialStore(store CredentialStore, box *encryption.Box) CredentialStore {
	if box == nil {
		return store
	}
	return &sealedCredentialStore{CredentialStore: store, box: box}
}

// credentialSecretFields points at every secret field of cred. API keys share
// one field name: they are interchangeable members of one rotation set.
func credentialSecretFields(cred *ManagedProviderCredential) []encryption.Field {
	fields := []encryption.Field{
		{Name: "service_account_json", Value: &cred.ServiceAccountJSON},
		{Name: "service_account_json_base64", Value: &cred.ServiceAccountJSONBase64},
		{Name: "proxy_url", Value: &cred.ProxyURL},
	}
	for i := range cred.APIKeys {
		fields = append(fields, encryption.Field{Name: "api_keys", Value: &cred.APIKeys[i]})
	}
	return fields
}

func (s *sealedCredentialStore) open(cred *ManagedProviderCredential) error {
	return s.box.OpenFields(credentialSecretKind, normalizeCredentialName(cred.Name), credentialSecretFields(cred)...)
}

func (s *sealedCredentialStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	creds, err := s.CredentialStore.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range creds {
		if err := s.open(&creds[i]); err != nil {
			return nil, err
		}
	}
	return creds, nil
}

func (s *sealedCredentialStore) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	cred, err := s.CredentialStore.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.open(cred); err != nil {
		return nil, err
	}
	return cred, nil
}

func (s *sealedCredentialStore) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	// The caller's slice must not end up holding ciphertext.
	cred.APIKeys = slices.Clone(cred.APIKeys)
	if err := s.box.SealFields(credentialSecretKind, normalizeCredentialName(cred.Name), credentialSecretFields(&cred)...); err != nil {
		return err
	}
	return s.CredentialStore.Upsert(ctx, cred)
}

// credentialSwapper replaces a credential's secret fields only while they
// still hold the values read earlier. Both store backends implement it.
type credentialSwapper interface {
	swapSecrets(ctx context.Context, current, next ManagedProviderCredential) (bool, error)
}

// reencrypt rewrites every row holding plaintext or a value sealed with an
// older data key. Each write is conditional on the secrets still being what
// was read, so a concurrent admin edit is never overwritten: the row is read
// again and retried.
func (s *sealedCredentialStore) reencrypt(ctx context.Context, swap credentialSwapper) (encryption.Report, error) {
	listed, err := s.CredentialStore.List(ctx)
	if err != nil {
		return encryption.Report{Entity: "provider_credentials"}, err
	}
	names := make([]string, len(listed))
	for i, row := range listed {
		names[i] = row.Name
	}
	return encryption.ReencryptRows("provider_credentials", names, func(name string) (encryption.RowOutcome, error) {
		return s.reencryptRow(ctx, swap, name)
	})
}

func (s *sealedCredentialStore) reencryptRow(ctx context.Context, swap credentialSwapper, name string) (encryption.RowOutcome, error) {
	current, err := s.CredentialStore.Get(ctx, name)
	if errors.Is(err, ErrCredentialNotFound) {
		return encryption.RowUnchanged, nil
	}
	if err != nil {
		return 0, err
	}
	if !s.box.NeedsReseal(credentialSecretFields(current)...) {
		return encryption.RowUnchanged, nil
	}
	next := *current
	next.APIKeys = slices.Clone(current.APIKeys)
	if err := s.open(&next); err != nil {
		return 0, err
	}
	if err := s.box.SealFields(credentialSecretKind, normalizeCredentialName(next.Name), credentialSecretFields(&next)...); err != nil {
		return 0, err
	}
	swapped, err := swap.swapSecrets(ctx, *current, next)
	if err != nil {
		return 0, err
	}
	if !swapped {
		return encryption.RowConflict, nil
	}
	return encryption.RowRewritten, nil
}
