package chatgpt

import (
	"encoding/base64"
	"strings"

	"github.com/goccy/go-json"
)

// accountIDClaim is the JWT claim the ChatGPT access token carries the
// subscription's account ID in. Codex sends that ID as chatgpt-account-id, so
// deriving it from the token keeps the provider single-credential.
const accountIDClaim = "https://api.openai.com/auth"

// accountIDFromToken extracts the ChatGPT account ID from an access token.
// It returns "" for anything that is not a JWT with that claim; the header is
// optional, so an unreadable token is not an error here.
func accountIDFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(claims[accountIDClaim], &auth); err != nil {
		return ""
	}
	return auth.AccountID
}
