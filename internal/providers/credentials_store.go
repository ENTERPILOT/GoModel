package providers

import (
	"strings"
	"time"

	"github.com/goccy/go-json"
)

func encodeCredentialList(value []string) (string, error) {
	if value == nil {
		value = []string{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeCredentialList(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var value []string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, nil
	}
	return value, nil
}

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
