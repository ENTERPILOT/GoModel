package googleauth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const DefaultScope = "https://www.googleapis.com/auth/cloud-platform"

type Config struct {
	AuthType                 string
	ServiceAccountFile       string
	ServiceAccountJSON       string
	ServiceAccountJSONBase64 string
	Scope                    string
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

func TokenSource(ctx context.Context, cfg Config) (oauth2.TokenSource, error) {
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
		credentials, err := serviceAccountJSON(cfg)
		if err != nil {
			return nil, err
		}
		jwtCfg, err := google.JWTConfigFromJSON(credentials, scope)
		if err != nil {
			return nil, fmt.Errorf("parse service account credentials: %w", err)
		}
		return jwtCfg.TokenSource(ctx), nil
	case "gcp_adc":
		source, err := google.DefaultTokenSource(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("load application default credentials: %w", err)
		}
		return source, nil
	}
	return nil, fmt.Errorf("unsupported Google auth type %q", authType)
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

func HTTPClient(base *http.Client, source oauth2.TokenSource) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone := *base
	clone.Transport = &oauth2.Transport{
		Source: oauth2.ReuseTokenSource(nil, source),
		Base:   transport,
	}
	return &clone
}
