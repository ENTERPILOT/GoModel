package exchange

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// InstructionsMessageID is the ID of the message built from a Responses
// request's instructions field.
const InstructionsMessageID = "instructions"

func originalID(index int) string {
	return "m" + strconv.Itoa(index)
}

// originalIndex resolves an "m<index>" message ID against a list of n
// original messages.
func originalIndex(id string, n int) (int, error) {
	rest, ok := strings.CutPrefix(id, "m")
	if !ok {
		return 0, fmt.Errorf("exchange: message %q is not an original message", id)
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 0 || idx >= n {
		return 0, fmt.Errorf("exchange: message %q does not match an original message", id)
	}
	return idx, nil
}

func choiceKey(index int) string {
	return "choice:" + strconv.Itoa(index)
}

func choiceIndex(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, "choice:")
	if !ok {
		return 0, false
	}
	idx, err := strconv.Atoi(rest)
	return idx, err == nil && idx >= 0
}

func randomID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "0"
	}
	return prefix + hex.EncodeToString(b[:])
}
