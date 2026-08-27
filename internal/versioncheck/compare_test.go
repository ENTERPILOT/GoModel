package versioncheck

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"patch bump", "0.1.81", "0.1.82", true},
		{"minor bump", "0.1.81", "0.2.0", true},
		{"major bump", "0.9.9", "1.0.0", true},
		{"same version", "0.1.81", "0.1.81", false},
		{"older manifest", "0.1.82", "0.1.81", false},
		{"double digit patch beats single", "0.1.9", "0.1.10", true},
		{"v prefix on both sides", "v0.1.81", "v0.1.82", true},
		{"pro release bump", "1.0.0-pro", "1.1.0-pro", true},
		{"same pro release", "1.0.0-pro", "1.0.0-pro", false},
		{"prerelease superseded by final", "1.0.0-rc1", "1.0.0", true},
		{"final not superseded by prerelease", "1.0.0", "1.0.0-rc1", false},
		{"build metadata ignored", "1.0.0+core.0.1.81", "1.0.0", false},
		{"build metadata does not mask a bump", "1.0.0+core.0.1.81", "1.0.1", true},
		{"shorter current padded with zeroes", "1.0", "1.0.1", true},
		{"dev build never reports an update", "dev", "0.1.82", false},
		{"empty current", "", "0.1.82", false},
		{"empty latest", "0.1.81", "", false},
		{"garbage manifest", "0.1.81", "<!doctype html>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}
