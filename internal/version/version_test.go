package version

import "testing"

func TestChannelFor(t *testing.T) {
	tests := []struct {
		app  string
		want string
	}{
		{AppCore, "core"},
		{AppPro, "pro"},
		{"gomodel pro", "pro"},
		{"  GoModel Pro  ", "pro"},
		{"Acme Gateway", "core"},
		{"", "core"},
	}
	for _, tt := range tests {
		t.Run(tt.app, func(t *testing.T) {
			if got := ChannelFor(tt.app); got != tt.want {
				t.Fatalf("ChannelFor(%q) = %q, want %q", tt.app, got, tt.want)
			}
		})
	}
}

// Channel must stay a thin wrapper so the update check and anything else
// reading the distribution name can never disagree about the manifest.
func TestChannelFollowsApp(t *testing.T) {
	original := App
	t.Cleanup(func() { App = original })

	App = AppPro
	if Channel() != "pro" {
		t.Fatalf("Channel() = %q with App=%q", Channel(), App)
	}
	App = AppCore
	if Channel() != "core" {
		t.Fatalf("Channel() = %q with App=%q", Channel(), App)
	}
}
