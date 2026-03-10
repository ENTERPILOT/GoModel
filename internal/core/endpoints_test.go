package core

import "testing"

func TestDescribeEndpointPath(t *testing.T) {
	tests := []struct {
		path      string
		managed   bool
		dialect   string
		operation string
		bodyMode  BodyMode
	}{
		{path: "/v1/chat/completions", managed: true, dialect: "openai_compat", operation: "chat_completions", bodyMode: BodyModeJSON},
		{path: "/v1/batches", managed: true, dialect: "openai_compat", operation: "batches", bodyMode: BodyModeJSON},
		{path: "/v1/files/file_1", managed: true, dialect: "openai_compat", operation: "files", bodyMode: BodyModeMultipart},
		{path: "/p/openai/responses", managed: true, dialect: "provider_passthrough", operation: "provider_passthrough", bodyMode: BodyModeOpaque},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DescribeEndpointPath(tt.path)
			if !got.ModelInteraction {
				t.Fatal("ModelInteraction = false, want true")
			}
			if got.IngressManaged != tt.managed {
				t.Fatalf("IngressManaged = %v, want %v", got.IngressManaged, tt.managed)
			}
			if got.Dialect != tt.dialect {
				t.Fatalf("Dialect = %q, want %q", got.Dialect, tt.dialect)
			}
			if got.Operation != tt.operation {
				t.Fatalf("Operation = %q, want %q", got.Operation, tt.operation)
			}
			if got.BodyMode != tt.bodyMode {
				t.Fatalf("BodyMode = %q, want %q", got.BodyMode, tt.bodyMode)
			}
		})
	}
}

func TestParseProviderPassthroughPath(t *testing.T) {
	provider, endpoint, ok := ParseProviderPassthroughPath("/p/anthropic/messages/batches")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", provider)
	}
	if endpoint != "messages/batches" {
		t.Fatalf("endpoint = %q, want messages/batches", endpoint)
	}
}
