package providers

import "sort"

// Credential form field names. They match the admin API's upsert payload keys
// and the ManagedProviderCredential fields they fill, so a schema entry can be
// used verbatim as an error `param` and as a dashboard form field id.
const (
	CredentialFieldAPIKeys                  = "api_keys"
	CredentialFieldSessionStickyKeys        = "session_sticky_keys"
	CredentialFieldBaseURL                  = "base_url"
	CredentialFieldAPIVersion               = "api_version"
	CredentialFieldBackend                  = "backend"
	CredentialFieldAuthType                 = "auth_type"
	CredentialFieldAPIMode                  = "api_mode"
	CredentialFieldVertexProject            = "vertex_project"
	CredentialFieldVertexLocation           = "vertex_location"
	CredentialFieldServiceAccountFile       = "service_account_file"
	CredentialFieldServiceAccountJSON       = "service_account_json"
	CredentialFieldServiceAccountJSONBase64 = "service_account_json_base64"
	CredentialFieldGCPScope                 = "gcp_scope"
	CredentialFieldModels                   = "models"
)

// CredentialField describes one field of a provider type's credential form.
// Only the fields a type actually reads are described, so a dashboard can
// render a form that fits the provider instead of every field the wire format
// happens to carry.
type CredentialField struct {
	Name     string
	Required bool
	// Advanced marks a field most operators leave at its default for this
	// provider type, so the form can fold it away behind a disclosure.
	Advanced bool
	// Options are the canonical values of an enumerated field, for a form to
	// offer as a choice. It is deliberately not a whitelist: providers accept
	// further spellings of each value (`useNativeAPI`, `NormalizeAuthType`,
	// ...), and rejecting those would make the admin API stricter than the
	// config.yaml and env vars it exists to replace. An empty value always
	// means "provider default", so it is never listed.
	Options []string
}

// CredentialSchema is the credential form one provider type accepts.
type CredentialSchema struct {
	Type           string
	DefaultBaseURL string
	Fields         []CredentialField
}

// Field returns the schema entry for name, and whether the type accepts it.
func (s CredentialSchema) Field(name string) (CredentialField, bool) {
	for _, field := range s.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return CredentialField{}, false
}

// Accepts reports whether this provider type uses the named credential field.
func (s CredentialSchema) Accepts(name string) bool {
	_, ok := s.Field(name)
	return ok
}

// CredentialSchemas returns the credential form of every registered provider
// type, ordered by type name.
func (f *ProviderFactory) CredentialSchemas() []CredentialSchema {
	f.mu.RLock()
	specs := make(map[string]DiscoveryConfig, len(f.builders))
	for providerType := range f.builders {
		specs[providerType] = f.discoveryConfigs[providerType]
	}
	f.mu.RUnlock()

	types := make([]string, 0, len(specs))
	for providerType := range specs {
		types = append(types, providerType)
	}
	sort.Strings(types)

	schemas := make([]CredentialSchema, 0, len(types))
	for _, providerType := range types {
		schemas = append(schemas, credentialSchema(providerType, specs[providerType]))
	}
	return schemas
}

// credentialSchema builds one provider type's credential form: the fields the
// registration declares, or the plain API-key shape derived from its discovery
// flags, plus the model list every provider type accepts.
func credentialSchema(providerType string, spec DiscoveryConfig) CredentialSchema {
	fields := spec.CredentialFields
	if len(fields) == 0 {
		fields = defaultCredentialFields(spec)
	}
	schema := CredentialSchema{Type: providerType, DefaultBaseURL: spec.DefaultBaseURL,
		Fields: make([]CredentialField, 0, len(fields)+2)}
	schema.Fields = append(schema.Fields, fields...)
	for _, field := range fields {
		if field.Name == CredentialFieldAPIKeys {
			schema.Fields = append(schema.Fields, CredentialField{Name: CredentialFieldSessionStickyKeys, Advanced: true})
			break
		}
	}
	schema.Fields = append(schema.Fields, CredentialField{Name: CredentialFieldModels, Advanced: true})
	return schema
}

// defaultCredentialFields covers every provider type that authenticates with
// an API key against one endpoint — the large majority. The base URL is folded
// away only when there is a key to fill in instead: for a keyless provider it
// is the one thing worth configuring, so hiding it would leave an empty form.
func defaultCredentialFields(spec DiscoveryConfig) []CredentialField {
	apiKeyRequired := !spec.AllowAPIKeyless
	fields := []CredentialField{
		{Name: CredentialFieldAPIKeys, Required: apiKeyRequired},
		{Name: CredentialFieldBaseURL, Required: spec.RequireBaseURL, Advanced: !spec.RequireBaseURL && apiKeyRequired},
	}
	if spec.SupportsAPIVersion {
		fields = append(fields, CredentialField{Name: CredentialFieldAPIVersion})
	}
	return fields
}

// credentialFieldValue reads the credential field a schema entry names, for
// the presence and enumeration checks in credential_validate.go. The two list
// fields report their first entry, which is all "is anything set here?" needs;
// api_keys has its own rules on top (validateCredentialAPIKeys).
func credentialFieldValue(cred ManagedProviderCredential, name string) string {
	switch name {
	case CredentialFieldAPIKeys:
		if len(cred.APIKeys) == 0 {
			return ""
		}
		return cred.APIKeys[0]
	case CredentialFieldBaseURL:
		return cred.BaseURL
	case CredentialFieldAPIVersion:
		return cred.APIVersion
	case CredentialFieldBackend:
		return cred.Backend
	case CredentialFieldAuthType:
		return cred.AuthType
	case CredentialFieldAPIMode:
		return cred.APIMode
	case CredentialFieldVertexProject:
		return cred.VertexProject
	case CredentialFieldVertexLocation:
		return cred.VertexLocation
	case CredentialFieldServiceAccountFile:
		return cred.ServiceAccountFile
	case CredentialFieldServiceAccountJSON:
		return cred.ServiceAccountJSON
	case CredentialFieldServiceAccountJSONBase64:
		return cred.ServiceAccountJSONBase64
	case CredentialFieldGCPScope:
		return cred.GCPScope
	case CredentialFieldModels:
		if len(cred.Models) == 0 {
			return ""
		}
		return cred.Models[0]
	default:
		return ""
	}
}
