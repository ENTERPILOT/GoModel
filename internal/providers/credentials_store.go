package providers

import (
	"strings"
	"time"
)

// stampCredentialUpsert sets timestamps: CreatedAt on insert, UpdatedAt always.
func stampCredentialUpsert(cred *ManagedProviderCredential) {
	now := time.Now().UTC()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = now
	}
	cred.UpdatedAt = now
}

// collectManagedProviderCredentials drains a row iterator into a slice.
func collectManagedProviderCredentials(next func() (ManagedProviderCredential, bool, error), rowsErr func() error) ([]ManagedProviderCredential, error) {
	result := make([]ManagedProviderCredential, 0)
	for {
		cred, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		result = append(result, cred)
	}
	if err := rowsErr(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeCredentialName(name string) string {
	return strings.TrimSpace(name)
}
