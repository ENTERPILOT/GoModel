package oauthlogin

import (
	"fmt"
	"net/url"
	"strings"
)

// Account kinds one oauthlogin provider instance can serve. The kind selects
// the shipped defaults for the device, token, and inference endpoints; every
// endpoint stays overridable, so an upstream that moves its URLs needs a
// configuration change and never a code change.
const (
	accountXAI    = "xai"
	accountCursor = "cursor"
	accountCodex  = "codex"
)

// accountKind is one supported account kind and the defaults it ships with.
type accountKind struct {
	// name is the canonical spelling operators configure.
	name string
	// deviceEndpoint starts a device authorization grant.
	deviceEndpoint string
	// tokenEndpoint redeems a device code and refreshes an access token.
	tokenEndpoint string
	// baseURL is the OpenAI-compatible inference endpoint the access token
	// authenticates against.
	baseURL string
	// scope is the default scope requested at the device endpoint. Empty
	// requests whatever the authorization server grants by default.
	scope string
}

// accountKinds lists the shipped kinds. Endpoints are defaults, not
// constants the code depends on: configuration replaces any of them.
var accountKinds = []accountKind{
	{
		name:           accountXAI,
		deviceEndpoint: "https://auth.x.ai/oauth/device/code",
		tokenEndpoint:  "https://auth.x.ai/oauth/token",
		baseURL:        "https://api.x.ai/v1",
	},
	{
		name:           accountCursor,
		deviceEndpoint: "https://authenticator.cursor.sh/oauth/device/code",
		tokenEndpoint:  "https://authenticator.cursor.sh/oauth/token",
		baseURL:        "https://api2.cursor.sh/v1",
	},
	{
		name:           accountCodex,
		deviceEndpoint: "https://auth.openai.com/oauth/device/code",
		tokenEndpoint:  "https://auth.openai.com/oauth/token",
		baseURL:        "https://chatgpt.com/backend-api/codex/v1",
		scope:          "openid profile email offline_access",
	},
}

// normalizeAccount maps an operator spelling onto the canonical kind name.
// An empty value is not a kind: the caller decides whether that is an error.
func normalizeAccount(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case accountXAI, "grok":
		return accountXAI, true
	case accountCursor:
		return accountCursor, true
	case accountCodex, "openai", "openai-codex":
		return accountCodex, true
	default:
		return "", false
	}
}

// accountDefaults returns the shipped defaults for a canonical kind name.
func accountDefaults(name string) (accountKind, bool) {
	for _, kind := range accountKinds {
		if kind.name == name {
			return kind, true
		}
	}
	return accountKind{}, false
}

// accountOptions lists the canonical kind names, for the credential form.
func accountOptions() []string {
	options := make([]string, len(accountKinds))
	for i, kind := range accountKinds {
		options[i] = kind.name
	}
	return options
}

// resolveAccount applies configuration over a kind's shipped defaults. An
// empty account takes the xAI defaults, so a provider configured with only
// endpoints still has something to call. An unknown account is rejected
// before any endpoint is trusted.
func resolveAccount(account, baseURL, deviceEndpoint, tokenEndpoint, scope string) (accountKind, error) {
	account = strings.TrimSpace(account)
	name := accountXAI
	if account != "" {
		canonical, ok := normalizeAccount(account)
		if !ok {
			return accountKind{}, fmt.Errorf("oauthlogin account %q is not supported; choose one of %s", account, strings.Join(accountOptions(), ", "))
		}
		name = canonical
	}
	resolved, _ := accountDefaults(name)
	resolved.baseURL = firstNonEmpty(baseURL, resolved.baseURL)
	resolved.deviceEndpoint = firstNonEmpty(deviceEndpoint, resolved.deviceEndpoint)
	resolved.tokenEndpoint = firstNonEmpty(tokenEndpoint, resolved.tokenEndpoint)
	resolved.scope = firstNonEmpty(scope, resolved.scope)
	return resolved, nil
}

// validateAccount reports why an account configuration cannot serve traffic.
// The device and token endpoints are required: without them there is no
// login and no refresh. The inference base URL is required too, since every
// request is built on it.
func validateAccount(kind accountKind) error {
	if err := requireHTTPURL("device authorization endpoint", kind.deviceEndpoint); err != nil {
		return err
	}
	if err := requireHTTPURL("token endpoint", kind.tokenEndpoint); err != nil {
		return err
	}
	if err := requireHTTPURL("base URL", kind.baseURL); err != nil {
		return err
	}
	return nil
}

// requireHTTPURL rejects an empty or non-HTTP(S) endpoint, without echoing
// the value: an endpoint URL can carry credentials.
func requireHTTPURL(what, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("oauthlogin %s must be an http(s) URL", what)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
