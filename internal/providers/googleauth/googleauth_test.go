package googleauth

import (
	"encoding/base64"
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
