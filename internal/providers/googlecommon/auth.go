// Package googlecommon holds infrastructure shared by GoModel's Google-backed
// providers (Gemini AI Studio + Vertex AI). It currently covers authentication
// (ADC / service-account TokenSource resolution + quota project propagation
// via X-Goog-User-Project) and Vertex URL transformations between the native
// publisher endpoint and the OpenAI-compatible endpoint.
package googlecommon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const DefaultScope = "https://www.googleapis.com/auth/cloud-platform"

// QuotaProjectHeader is the HTTP header Google APIs read to determine which
// project to bill and count quota against. Required for ADC user credentials
// against APIs like aiplatform.googleapis.com; harmless (and routinely set)
// for service-account credentials.
const QuotaProjectHeader = "X-Goog-User-Project"

type Config struct {
	AuthType                 string
	ServiceAccountFile       string
	ServiceAccountJSON       string
	ServiceAccountJSONBase64 string
	Scope                    string
}

// Credentials bundles the OAuth2 token source with the project ID resolved
// from the credentials material. For ADC user credentials this is the
// `quota_project_id` written by `gcloud auth application-default
// set-quota-project`; for service accounts it is the SA JSON `project_id`.
// QuotaProjectID may be empty when neither source supplies one.
type Credentials struct {
	TokenSource    oauth2.TokenSource
	QuotaProjectID string
}

func NormalizeAuthType(authType string, hasServiceAccount bool) string {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "gcp_service_account", "service_account":
		return "gcp_service_account"
	case "gcp_adc", "adc", "google_adc":
		return "gcp_adc"
	default:
		if hasServiceAccount {
			return "gcp_service_account"
		}
		return "gcp_adc"
	}
}

func HasServiceAccount(cfg Config) bool {
	return strings.TrimSpace(cfg.ServiceAccountJSONBase64) != "" ||
		strings.TrimSpace(cfg.ServiceAccountJSON) != "" ||
		strings.TrimSpace(cfg.ServiceAccountFile) != ""
}

// FindCredentials resolves a TokenSource and QuotaProjectID from either a
// service-account credential (file, raw JSON, or base64 JSON) or Application
// Default Credentials. The returned QuotaProjectID flows from ADC
// `quota_project_id` or SA `project_id` and is intended to be sent as the
// X-Goog-User-Project header on outbound API calls; callers may override it
// when their resource-owning project differs from the auth project.
func FindCredentials(ctx context.Context, cfg Config) (*Credentials, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope := strings.TrimSpace(cfg.Scope)
	if scope == "" {
		scope = DefaultScope
	}

	authType := NormalizeAuthType(cfg.AuthType, HasServiceAccount(cfg))
	switch authType {
	case "gcp_service_account":
		credBytes, err := serviceAccountJSON(cfg)
		if err != nil {
			return nil, err
		}
		creds, err := google.CredentialsFromJSONWithType(ctx, credBytes, google.ServiceAccount, scope)
		if err != nil {
			return nil, fmt.Errorf("parse service account credentials: %w", err)
		}
		return &Credentials{TokenSource: creds.TokenSource, QuotaProjectID: resolveQuotaProject(creds)}, nil
	case "gcp_adc":
		creds, err := google.FindDefaultCredentials(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("load application default credentials: %w", err)
		}
		return &Credentials{TokenSource: creds.TokenSource, QuotaProjectID: resolveQuotaProject(creds)}, nil
	}
	return nil, fmt.Errorf("unsupported Google auth type %q", authType)
}

// resolveQuotaProject returns the project that should be sent as
// X-Goog-User-Project. For service-account credentials this is the `project_id`
// already surfaced on *google.Credentials. For authorized-user ADC the public
// Credentials struct leaves ProjectID empty, so we parse `quota_project_id`
// from the raw JSON ourselves — that field is what `gcloud auth
// application-default set-quota-project` writes and is required for ADC
// access to APIs like aiplatform.googleapis.com.
func resolveQuotaProject(creds *google.Credentials) string {
	if creds == nil {
		return ""
	}
	if project := strings.TrimSpace(creds.ProjectID); project != "" {
		return project
	}
	if len(creds.JSON) == 0 {
		return ""
	}
	var raw struct {
		QuotaProjectID string `json:"quota_project_id"`
	}
	if err := json.Unmarshal(creds.JSON, &raw); err != nil {
		return ""
	}
	return strings.TrimSpace(raw.QuotaProjectID)
}

func serviceAccountJSON(cfg Config) ([]byte, error) {
	if value := strings.TrimSpace(cfg.ServiceAccountJSONBase64); value != "" {
		decoded, err := decodeServiceAccountBase64(value)
		if err != nil {
			return nil, fmt.Errorf("decode service account JSON: %w", err)
		}
		return decoded, nil
	}
	if value := strings.TrimSpace(cfg.ServiceAccountJSON); value != "" {
		return []byte(value), nil
	}
	if path := strings.TrimSpace(cfg.ServiceAccountFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read service account file: %w", err)
		}
		return contents, nil
	}
	return nil, fmt.Errorf("service account credentials are required")
}

func decodeServiceAccountBase64(value string) ([]byte, error) {
	decoded, stdErr := base64.StdEncoding.DecodeString(value)
	if stdErr == nil {
		return decoded, nil
	}

	for _, encoding := range []*base64.Encoding{
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	if padded := paddedBase64(value); padded != value {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if decoded, err := encoding.DecodeString(padded); err == nil {
				return decoded, nil
			}
		}
	}
	return nil, fmt.Errorf("standard base64 decode failed: %w; also tried raw standard, URL-safe, raw URL-safe, and padded variants", stdErr)
}

func paddedBase64(value string) string {
	switch remainder := len(value) % 4; remainder {
	case 0:
		return value
	case 2:
		return value + "=="
	case 3:
		return value + "="
	default:
		return value
	}
}

// HTTPClient returns an *http.Client that injects bearer tokens from source on
// every request and, when quotaProject is non-empty, also sets the
// X-Goog-User-Project header. The latter is required for ADC user-credential
// flows against APIs like aiplatform.googleapis.com that demand an explicit
// billing project; pass an empty string to skip.
func HTTPClient(base *http.Client, source oauth2.TokenSource, quotaProject string) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var rt http.RoundTripper = &oauth2.Transport{
		Source: oauth2.ReuseTokenSource(nil, source),
		Base:   transport,
	}
	if project := strings.TrimSpace(quotaProject); project != "" {
		rt = &quotaProjectTransport{base: rt, project: project}
	}
	clone := *base
	clone.Transport = rt
	return &clone
}

// quotaProjectTransport sets the X-Goog-User-Project header on outbound
// requests before delegating. The request is cloned so the caller's struct is
// not mutated.
type quotaProjectTransport struct {
	base    http.RoundTripper
	project string
}

func (t *quotaProjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set(QuotaProjectHeader, t.project)
	return t.base.RoundTrip(clone)
}
