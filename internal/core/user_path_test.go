package core

import "testing"

func TestNormalizeUserPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty stays unset", raw: "", want: ""},
		{name: "trim add leading slash remove trailing slash", raw: " team/a/b/ ", want: "/team/a/b"},
		{name: "collapse repeated slashes", raw: "/team//a///b", want: "/team/a/b"},
		{name: "root stays root", raw: "/", want: "/"},
		{name: "reject current dir segment", raw: "/team/./a", wantErr: true},
		{name: "reject parent dir segment", raw: "/team/../a", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeUserPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NormalizeUserPath() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeUserPath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeUserPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserPathAncestors(t *testing.T) {
	t.Parallel()

	got := UserPathAncestors("/team/a/user")
	want := []string{"/team/a/user", "/team/a", "/team", "/"}

	if len(got) != len(want) {
		t.Fatalf("len(UserPathAncestors()) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UserPathAncestors()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
