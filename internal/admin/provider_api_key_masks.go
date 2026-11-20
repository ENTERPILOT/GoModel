package admin

import (
	"strconv"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// apiKeyMaskSlotSeparator splits a masked API key from the 1-based position
// of the stored key it stands for: "***********#2" is the second stored key.
const apiKeyMaskSlotSeparator = "#"

// redactAPIKeys keeps the entry count (so operators can see how many keys are
// configured) but masks every literal value; secret references are kept. Each
// mask names its stored position, so a client that removes or reorders masked
// entries still keeps the keys it meant to.
func redactAPIKeys(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	redacted := make([]string, len(values))
	for i, value := range values {
		redacted[i] = redactCredentialValue(value)
		if redacted[i] == redactedCredentialValue {
			redacted[i] += apiKeyMaskSlotSeparator + strconv.Itoa(i+1)
		}
	}
	return redacted
}

// mergeRedactedAPIKeys resolves masked entries in api_keys against the stored
// list and also returns the stored keys it resolved that way. A mask naming a
// position ("***********#2") keeps that stored key wherever it now sits; a
// bare all-asterisk mask, as older clients send, keeps the stored key at its
// own index. A mask with no stored key behind it is rejected, so the mask is
// never saved as a key.
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
// stored index it stands for; a bare mask stands for its own index. A mask
// with an unreadable position returns -1, which matches no stored key.
func maskedAPIKeySlot(value string, index int) (int, bool) {
	mask, slot, hasSlot := strings.Cut(value, apiKeyMaskSlotSeparator)
	if !isRedactedCredentialValue(mask) {
		return 0, false
	}
	if !hasSlot {
		return index, true
	}
	n, err := strconv.Atoi(slot)
	if err != nil || n < 1 {
		return -1, true
	}
	return n - 1, true
}
