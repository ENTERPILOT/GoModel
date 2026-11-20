package admin

import (
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

const (
	// apiKeyMaskSlotSeparator splits a masked API key from the 1-based
	// position of the stored key it stands for: "...#2" is the second key.
	apiKeyMaskSlotSeparator = "#"
	// A masked API key shows its first and last few characters so operators
	// can tell keys apart; the prefix usually names the key type ("sk-pr").
	apiKeyHintPrefixLength = 5
	apiKeyHintSuffixLength = 3
	// apiKeyHintMinLength is the shortest key that shows a hint; a shorter
	// key would reveal too much of itself, so it is fully masked.
	apiKeyHintMinLength = 20
)

// redactAPIKeys keeps the entry count (so operators can see how many keys are
// configured) but masks every literal value; secret references are kept. A
// mask shows the key's first and last few characters and names its stored
// position ("sk-pr***********7Qa#2"), so a client that removes or reorders
// masked entries still keeps the keys it meant to.
func redactAPIKeys(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	redacted := make([]string, len(values))
	for i, value := range values {
		redacted[i] = redactCredentialValue(value)
		if redacted[i] == redactedCredentialValue {
			redacted[i] = apiKeyHint(value) + apiKeyMaskSlotSeparator + strconv.Itoa(i+1)
		}
	}
	return redacted
}

// apiKeyHint masks the middle of a key behind a fixed-width run of asterisks,
// so the mask does not reveal the key's length.
func apiKeyHint(value string) string {
	runes := []rune(value)
	if len(runes) < apiKeyHintMinLength {
		return redactedCredentialValue
	}
	return string(runes[:apiKeyHintPrefixLength]) + redactedCredentialValue + string(runes[len(runes)-apiKeyHintSuffixLength:])
}

// mergeRedactedAPIKeys resolves masked entries in api_keys against the stored
// list and also returns the stored keys it resolved that way. A mask naming a
// position ("sk-pr***********7Qa#2") keeps that stored key wherever it now
// sits; its visible characters are display only. A bare all-asterisk mask,
// as older clients send, keeps the stored key at its own index. A mask with
// no stored key behind it is rejected, so a mask is never saved as a key.
func mergeRedactedAPIKeys(incoming, stored []string) (merged, kept []string, err error) {
	if incoming == nil {
		return nil, nil, nil
	}
	merged = make([]string, len(incoming))
	for i, value := range incoming {
		slot, masked := maskedAPIKeySlot(value, i)
		if !masked {
			merged[i] = value
			continue
		}
		if slot < 0 || slot >= len(stored) {
			return nil, nil, core.NewInvalidRequestError("api_keys entry is redacted but has no stored value to preserve; provide the real value", nil).
				WithParam(providers.CredentialFieldAPIKeys)
		}
		merged[i] = stored[slot]
		kept = append(kept, stored[slot])
	}
	return merged, kept, nil
}

// maskedAPIKeySlot reports whether value is a mask and, if so, the 0-based
// stored index it stands for; a bare mask stands for its own index. Any other
// value holding a run of asterisks is treated as a mask with an unreadable
// position and returns -1, which matches no stored key: real keys never
// contain one, and saving an edited mask as a key would break the provider.
func maskedAPIKeySlot(value string, index int) (int, bool) {
	if isRedactedCredentialValue(value) {
		return index, true
	}
	if !strings.Contains(value, "***") {
		return 0, false
	}
	sep := strings.LastIndex(value, apiKeyMaskSlotSeparator)
	if sep < 0 {
		return -1, true
	}
	n, err := strconv.Atoi(value[sep+1:])
	if err != nil || n < 1 {
		return -1, true
	}
	return n - 1, true
}
