package providers

import (
	"fmt"
	"strings"
)

// CredentialFieldError reports a credential row the operator has to fix,
// naming the credential field at fault so the admin API can point the
// dashboard at that input instead of at the form as a whole.
type CredentialFieldError struct {
	Field   string
	Message string
}

func (e *CredentialFieldError) Error() string { return e.Message }

// credentialFieldError is a small constructor keeping call sites readable.
func credentialFieldError(field, format string, args ...any) *CredentialFieldError {
	return &CredentialFieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// validateCredential checks one row against its provider type's credential
// form before anything is persisted or registered, so an incomplete provider
// is rejected with a precise, field-scoped message rather than saved and then
// silently unroutable.
func validateCredential(cred ManagedProviderCredential, schema CredentialSchema) error {
	if err := validateCredentialAPIKeys(cred, schema); err != nil {
		return err
	}
	// Google's project/service-account auth replaces the "one required field
	// per row" shape with a choice between several valid combinations, so it
	// gets its own rules.
	if raw := cred.toRawProviderConfig(); isVertexProviderConfig(raw) {
		return validateGoogleCredential(cred)
	}
	for _, field := range schema.Fields {
		if !field.Required || field.Name == CredentialFieldAPIKeys {
			continue
		}
		if strings.TrimSpace(credentialFieldValue(cred, field.Name)) == "" {
			return credentialFieldError(field.Name, "%s is required for provider type %q", credentialFieldNoun(field.Name), cred.Type)
		}
	}
	return nil
}

// credentialFieldNoun reads a required field's name as prose. Unknown fields
// fall back to the wire name, which is still the clearest thing to say about
// a field this code has no words for.
func credentialFieldNoun(name string) string {
	switch name {
	case CredentialFieldBaseURL:
		return "a base URL"
	case CredentialFieldAPIVersion:
		return "an API version"
	default:
		return name
	}
}

// unappliableCredentialError explains a credential that passed the form's
// rules but still did not resolve into a routable provider — the combinations
// only the resolution pipeline knows about, such as a Gemini row whose Vertex
// fields are set without a backend to match. That is bad input, not a server
// fault, so it stays a field-scoped rejection: the API key is what resolution
// asks for whenever a type takes one.
func unappliableCredentialError(cred ManagedProviderCredential, schema CredentialSchema, err error) error {
	message := fmt.Sprintf("provider %q could not be applied: %v", cred.Name, err)
	if schema.Accepts(CredentialFieldAPIKeys) {
		return &CredentialFieldError{Field: CredentialFieldAPIKeys, Message: message}
	}
	return &CredentialFieldError{Message: message}
}

// validateCredentialAPIKeys enforces the rotation list's own rules: no blank
// entries (a blank key would silently send unauthenticated requests), and at
// least one key when the provider type has no other way to authenticate.
func validateCredentialAPIKeys(cred ManagedProviderCredential, schema CredentialSchema) error {
	field, accepted := schema.Field(CredentialFieldAPIKeys)
	if !accepted {
		return nil
	}
	for _, key := range cred.APIKeys {
		if strings.TrimSpace(key) == "" {
			return credentialFieldError(CredentialFieldAPIKeys, "API key entries cannot be blank")
		}
	}
	if field.Required && len(cred.APIKeys) == 0 {
		return credentialFieldError(CredentialFieldAPIKeys, "at least one API key is required for provider type %q", cred.Type)
	}
	return nil
}

// validateGoogleCredential covers Vertex and Vertex-backed Gemini: the
// endpoint comes from either an explicit base URL or the project/location
// pair, and a service-account auth type needs a key to read.
func validateGoogleCredential(cred ManagedProviderCredential) error {
	if !HasResolvedProviderValue(cred.BaseURL) {
		if !HasResolvedProviderValue(cred.VertexProject) {
			return credentialFieldError(CredentialFieldVertexProject, "vertex_project is required unless a base URL is set")
		}
		if !HasResolvedProviderValue(cred.VertexLocation) {
			return credentialFieldError(CredentialFieldVertexLocation, "vertex_location is required unless a base URL is set")
		}
	}
	// An API key does not authenticate against Vertex, so "api_key" is valid
	// on a Gemini row only while it talks to AI Studio — which is not this
	// code path.
	switch strings.ToLower(strings.TrimSpace(cred.AuthType)) {
	case "", "gcp_adc", "adc", "google_adc":
		return nil
	case "gcp_service_account", "service_account":
		if HasResolvedProviderValue(cred.ServiceAccountFile) ||
			HasResolvedProviderValue(cred.ServiceAccountJSON) ||
			HasResolvedProviderValue(cred.ServiceAccountJSONBase64) {
			return nil
		}
		return credentialFieldError(CredentialFieldServiceAccountJSON, "a service account file, JSON, or base64 JSON is required when auth_type is gcp_service_account")
	default:
		return credentialFieldError(CredentialFieldAuthType, "auth_type %q cannot authenticate against Vertex; use gcp_adc or gcp_service_account", cred.AuthType)
	}
}
