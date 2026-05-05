package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceAccountJSONDecodesURLSafeBase64(t *testing.T) {
	want := []byte{0xfb, 0xff, 0xfe}
	encoded := base64.RawURLEncoding.EncodeToString(want)

	got, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded})
	if err != nil {
		t.Fatalf("serviceAccountJSON() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("decoded bytes = %q, want %q", string(got), string(want))
	}
}

func TestServiceAccountJSONDecodesPaddedURLSafeBase64(t *testing.T) {
	want := []byte{0xfb, 0xff, 0xfe}
	encoded := base64.URLEncoding.EncodeToString(want)

	got, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded})
	if err != nil {
		t.Fatalf("serviceAccountJSON() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("decoded bytes = %q, want %q", string(got), string(want))
	}
}

func TestServiceAccountJSONReportsOriginalBase64DecodeError(t *testing.T) {
	_, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: "not valid base64!"})
	if err == nil {
		t.Fatal("expected invalid base64 error")
	}
	if !strings.Contains(err.Error(), "standard base64 decode failed") {
		t.Fatalf("error = %v, want standard decode context", err)
	}
}

func TestTokenSourceAndHTTPClientAuthSelection(t *testing.T) {
	tests := []struct {
		name        string
		cfg         func(t *testing.T, tokenURL string) Config
		wantToken   string
		wantScope   string
		wantADCFile bool
	}{
		{
			name: "service account base64 uses service account path and default scope",
			cfg: func(t *testing.T, tokenURL string) Config {
				credentials := serviceAccountCredentials(t, tokenURL)
				encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
				if _, err := serviceAccountJSON(Config{ServiceAccountJSONBase64: encoded}); err != nil {
					t.Fatalf("serviceAccountJSON() error = %v", err)
				}
				return Config{ServiceAccountJSONBase64: encoded}
			},
			wantToken: "service-account-token",
			wantScope: DefaultScope,
		},
		{
			name: "empty service account config uses ADC path",
			cfg: func(t *testing.T, tokenURL string) Config {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcCredentialsFile(t, tokenURL))
				return Config{Scope: "https://www.googleapis.com/auth/custom"}
			},
			wantToken:   "adc-token",
			wantADCFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotForm url.Values
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm() error = %v", err)
				}
				gotForm = r.PostForm
				token := tt.wantToken
				if gotForm.Get("grant_type") == "urn:ietf:params:oauth:grant-type:jwt-bearer" {
					token = "service-account-token"
				} else if gotForm.Get("grant_type") == "refresh_token" {
					token = "adc-token"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": token,
					"token_type":   "Bearer",
					"expires_in":   3600,
				})
			}))
			defer tokenServer.Close()

			source, err := TokenSource(context.Background(), tt.cfg(t, tokenServer.URL))
			if err != nil {
				t.Fatalf("TokenSource() error = %v", err)
			}

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer "+tt.wantToken {
					t.Fatalf("Authorization = %q, want Bearer %s", got, tt.wantToken)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()

			client := HTTPClient(upstream.Client(), source)
			resp, err := client.Get(upstream.URL)
			if err != nil {
				t.Fatalf("HTTPClient request error = %v", err)
			}
			_ = resp.Body.Close()

			if tt.wantScope != "" {
				scope := tokenRequestScope(t, gotForm)
				if scope != tt.wantScope {
					t.Fatalf("scope = %q, want %q", scope, tt.wantScope)
				}
			}
			if tt.wantADCFile && gotForm.Get("refresh_token") != "adc-refresh-token" {
				t.Fatalf("refresh_token = %q, want adc-refresh-token", gotForm.Get("refresh_token"))
			}
		})
	}
}

func adcCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	contents := map[string]string{
		"type":          "authorized_user",
		"client_id":     "adc-client-id",
		"client_secret": "adc-client-secret",
		"refresh_token": "adc-refresh-token",
		"token_uri":     tokenURL,
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("failed to marshal ADC credentials: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("failed to write ADC credentials: %v", err)
	}
	return path
}

func serviceAccountCredentials(t *testing.T, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal test RSA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})
	contents := map[string]string{
		"type":           "service_account",
		"client_email":   "service@example.com",
		"private_key_id": "test-key-id",
		"private_key":    string(keyPEM),
		"token_uri":      tokenURL,
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("failed to marshal service account credentials: %v", err)
	}
	return string(encoded)
}

func tokenRequestScope(t *testing.T, form url.Values) string {
	t.Helper()
	if scope := form.Get("scope"); scope != "" {
		return scope
	}
	assertion := form.Get("assertion")
	parts := strings.Split(assertion, ".")
	if len(parts) < 2 {
		t.Fatalf("JWT assertion = %q, want header.payload.signature", assertion)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("failed to decode JWT payload: %v", err)
	}
	var claims struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("failed to decode JWT claims: %v", err)
	}
	return claims.Scope
}
