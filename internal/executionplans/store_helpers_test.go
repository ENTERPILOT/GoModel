package executionplans

import "testing"

func TestStoredScopeUserPath(t *testing.T) {
	tests := []struct {
		name     string
		scopeKey string
		userPath string
		want     string
	}{
		{
			name:     "global stays empty",
			scopeKey: "global",
			userPath: "",
			want:     "",
		},
		{
			name:     "legacy root path scope falls back to root",
			scopeKey: "path:/",
			userPath: "",
			want:     "/",
		},
		{
			name:     "legacy provider path scope restores exact path",
			scopeKey: "provider_path:openai:/team",
			userPath: "",
			want:     "/team",
		},
		{
			name:     "legacy provider model path scope restores exact path",
			scopeKey: "provider_model_path:openai:gpt-5:/team/a",
			userPath: "",
			want:     "/team/a",
		},
		{
			name:     "stored user path wins",
			scopeKey: "provider_model_path:openai:gpt-5:/team/a",
			userPath: "/tenant/root",
			want:     "/tenant/root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storedScopeUserPath(tt.scopeKey, tt.userPath); got != tt.want {
				t.Fatalf("storedScopeUserPath(%q, %q) = %q, want %q", tt.scopeKey, tt.userPath, got, tt.want)
			}
		})
	}
}
