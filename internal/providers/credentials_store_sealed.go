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

// credentialSealedFields points at every secret field of cred. API keys share
// one field name: they are interchangeable members of one rotation set.
func credentialSealedFields(cred *ManagedProviderCredential) []encryption.Field {
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
	return s.box.OpenFields(credentialSecretKind, normalizeCredentialName(cred.Name), credentialSealedFields(cred)...)
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
	if err := s.box.SealFields(credentialSecretKind, normalizeCredentialName(cred.Name), credentialSealedFields(&cred)...); err != nil {
		return err
	}
	return s.CredentialStore.Upsert(ctx, cred)
}

// reencrypt rewrites every row holding plaintext or a value sealed with an
// older data key. Each row is re-read just before it is rewritten, so an edit
// or delete made since the listing is not overwritten with the listed copy.
func (s *sealedCredentialStore) reencrypt(ctx context.Context) (encryption.Report, error) {
	report := encryption.Report{Entity: "provider_credentials"}
	listed, err := s.CredentialStore.List(ctx)
	if err != nil {
		return report, err
	}
	report.Rows = len(listed)
	for _, row := range listed {
		cred, err := s.CredentialStore.Get(ctx, row.Name)
		if errors.Is(err, ErrCredentialNotFound) {
			continue
		}
		if err != nil {
			return report, err
		}
		if !s.box.NeedsReseal(credentialSealedFields(cred)...) {
			continue
		}
		if err := s.open(cred); err != nil {
			return report, err
		}
		if err := s.Upsert(ctx, *cred); err != nil {
			return report, err
		}
		report.Reencrypted++
	}
	return report, nil
}
