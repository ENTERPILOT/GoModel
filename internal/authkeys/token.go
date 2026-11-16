package authkeys

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// NormalizeTokenPrefix trims prefix and checks it can start a managed key.
// Empty means DefaultTokenPrefix. A prefix is 1 to MaxTokenPrefixLength
// ASCII letters, digits, "_" or "-": keys travel in Authorization headers and
// get pasted into shells and YAML, so nothing in one may need quoting. Errors
// never repeat the value, in case a key was pasted where the prefix belongs.
func NormalizeTokenPrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return DefaultTokenPrefix, nil
	}
	if len(prefix) > MaxTokenPrefixLength {
		return "", fmt.Errorf("must be at most %d characters", MaxTokenPrefixLength)
	}
	if !validTokenPrefix(prefix) {
		return "", errors.New("may contain only ASCII letters, digits, \"_\" and \"-\"")
	}
	return prefix, nil
}

// validTokenPrefix reports whether prefix has the shape NormalizeTokenPrefix
// accepts.
func validTokenPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > MaxTokenPrefixLength {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// generateTokenMaterial issues a key under prefix: the prefix followed by
// secretLength base64url characters. Only the secret is hashed, so the stored
// hash does not depend on the prefix.
func generateTokenMaterial(prefix string) (value string, redactedValue string, secretHash string, err error) {
	secretBytesBuf := make([]byte, secretBytes)
	if _, err := rand.Read(secretBytesBuf); err != nil {
		return "", "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytesBuf)
	value = prefix + secret
	return value, redactTokenValue(prefix, value), hashSecret(secret), nil
}

// issuedTokenSecrets returns the parts of token a GoModel-issued key may be
// hashed under, or "" for a part that does not apply:
//
//   - legacy is everything after DefaultTokenPrefix, exactly as keys have
//     always been checked, so a default-prefix key with any secret length
//     keeps working;
//   - fixed is the last secretLength characters when what precedes them is a
//     valid prefix. Every issued secret has that length, so this finds the
//     secret of a key issued under any prefix, including one configured
//     before the current one. It is "" when it equals legacy.
func issuedTokenSecrets(token string) (legacy, fixed string) {
	if rest, ok := strings.CutPrefix(token, DefaultTokenPrefix); ok {
		legacy = rest
	}
	if len(token) > secretLength {
		split := len(token) - secretLength
		if secret := token[split:]; secret != legacy && validTokenPrefix(token[:split]) {
			fixed = secret
		}
	}
	return legacy, fixed
}

// looksLikeLiteLLMToken reports whether token has the shape of a LiteLLM virtual key,
// whose imported hash covers the whole token.
func looksLikeLiteLLMToken(token string) bool {
	return len(token) > len(liteLLMTokenPrefix) && strings.HasPrefix(token, liteLLMTokenPrefix)
}

func redactTokenValue(prefix, value string) string {
	if len(value) <= len(prefix)+4 {
		return prefix + "..."
	}
	return prefix + "..." + value[len(value)-4:]
}
