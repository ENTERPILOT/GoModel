package modelselectors

import "testing"

type selectorTestCatalog struct{ names []string }

func (c selectorTestCatalog) ProviderNames() []string { return c.names }

type selectorCase struct {
	name         string
	providers    []string
	raw          string
	want         Selector
	wantScope    ScopeKind
	wantExactKey string
	wantErr      bool
}

func TestNormalizeInputWithProviderNames(t *testing.T) {
	tests := []selectorCase{
		{name: "empty input", providers: []string{"prov"}, raw: " ", wantErr: true},
		{name: "global", providers: []string{"prov"}, raw: " / ", want: Selector{Selector: "/"}, wantScope: ScopeGlobal},
		{name: "provider only", providers: []string{"prov"}, raw: " prov/ ", want: Selector{Selector: "prov/", ProviderName: "prov"}, wantScope: ScopeProvider},
		{name: "model only", providers: []string{"prov"}, raw: " model ", want: Selector{Selector: "model", Model: "model"}, wantScope: ScopeModel},
		{
			name:         "provider model",
			providers:    []string{"prov"},
			raw:          " prov/model ",
			want:         Selector{Selector: "prov/model", ProviderName: "prov", Model: "model"},
			wantScope:    ScopeProviderModel,
			wantExactKey: "prov/model",
		},
		{
			name:      "slash-shaped model without provider catalog",
			raw:       "org/model",
			want:      Selector{Selector: "org/model", Model: "org/model"},
			wantScope: ScopeModel,
		},
		{
			name:      "slash-shaped model with unknown provider",
			providers: []string{"prov"},
			raw:       "unknown/model",
			want:      Selector{Selector: "unknown/model", Model: "unknown/model"},
			wantScope: ScopeModel,
		},
		{
			name:         "trimmed provider names",
			providers:    []string{" prov "},
			raw:          "prov/model",
			want:         Selector{Selector: "prov/model", ProviderName: "prov", Model: "model"},
			wantScope:    ScopeProviderModel,
			wantExactKey: "prov/model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeInputWithProviderNames(tt.providers, tt.raw)
			assertNormalizeResult(t, got, err, tt)
		})
	}
}

func TestNormalizeInputUsesCatalogProviderNames(t *testing.T) {
	tests := []struct {
		name    string
		catalog Catalog
		input   selectorCase
	}{
		{
			name:    "nil catalog treats slash input as model id",
			catalog: nil,
			input: selectorCase{
				raw:       "org/model",
				want:      Selector{Selector: "org/model", Model: "org/model"},
				wantScope: ScopeModel,
			},
		},
		{
			name:    "catalog provider creates exact selector",
			catalog: selectorTestCatalog{names: []string{"prov"}},
			input: selectorCase{
				raw:          "prov/model",
				want:         Selector{Selector: "prov/model", ProviderName: "prov", Model: "model"},
				wantScope:    ScopeProviderModel,
				wantExactKey: "prov/model",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeInput(tt.catalog, tt.input.raw)
			assertNormalizeResult(t, got, err, tt.input)
		})
	}
}

func TestProviderNames(t *testing.T) {
	if got := ProviderNames(nil); got != nil {
		t.Fatalf("ProviderNames(nil) = %#v, want nil", got)
	}

	names := []string{"prov"}
	got := ProviderNames(selectorTestCatalog{names: names})
	if len(got) != 1 || got[0] != "prov" {
		t.Fatalf("ProviderNames() = %#v, want [prov]", got)
	}
	got[0] = "changed"
	if names[0] != "prov" {
		t.Fatalf("ProviderNames() did not return a copy; original names = %#v", names)
	}
}

func TestNormalizeStored(t *testing.T) {
	tests := []struct {
		name         string
		selector     string
		providerName string
		model        string
		input        selectorCase
	}{
		{name: "empty stored parts", input: selectorCase{wantErr: true}},
		{name: "global stored selector", selector: " / ", input: selectorCase{want: Selector{Selector: "/"}, wantScope: ScopeGlobal}},
		{
			name:         "provider column without selector",
			providerName: " prov ",
			input:        selectorCase{want: Selector{Selector: "prov/", ProviderName: "prov"}, wantScope: ScopeProvider},
		},
		{
			name:  "model column without selector",
			model: " model ",
			input: selectorCase{want: Selector{Selector: "model", Model: "model"}, wantScope: ScopeModel},
		},
		{
			name:         "columns override stale selector",
			selector:     "old",
			providerName: "prov",
			model:        "model",
			input: selectorCase{
				want:         Selector{Selector: "prov/model", ProviderName: "prov", Model: "model"},
				wantScope:    ScopeProviderModel,
				wantExactKey: "prov/model",
			},
		},
		{
			name:     "legacy provider model selector",
			selector: "prov/model",
			input: selectorCase{
				want:         Selector{Selector: "prov/model", ProviderName: "prov", Model: "model"},
				wantScope:    ScopeProviderModel,
				wantExactKey: "prov/model",
			},
		},
		{name: "legacy provider selector", selector: "prov/", input: selectorCase{want: Selector{Selector: "prov/", ProviderName: "prov"}, wantScope: ScopeProvider}},
		{name: "legacy model selector", selector: "model", input: selectorCase{want: Selector{Selector: "model", Model: "model"}, wantScope: ScopeModel}},
		{
			name:     "legacy slash-shaped model is best-effort provider model",
			selector: "org/model",
			input: selectorCase{
				want:         Selector{Selector: "org/model", ProviderName: "org", Model: "model"},
				wantScope:    ScopeProviderModel,
				wantExactKey: "org/model",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeStored(tt.selector, tt.providerName, tt.model)
			assertNormalizeResult(t, got, err, tt.input)
		})
	}
}

func TestParseStoredParts(t *testing.T) {
	tests := []struct {
		name         string
		selector     string
		wantProvider string
		wantModel    string
	}{
		{name: "empty"},
		{name: "global", selector: "/"},
		{name: "provider only", selector: "prov/", wantProvider: "prov"},
		{name: "provider model", selector: "prov/model", wantProvider: "prov", wantModel: "model"},
		{name: "model only", selector: "model", wantModel: "model"},
		{name: "trims parts", selector: " prov / model ", wantProvider: "prov", wantModel: "model"},
		{name: "slash-shaped model legacy fallback", selector: "org/model/variant", wantProvider: "org", wantModel: "model/variant"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerName, model := ParseStoredParts(tt.selector)
			if providerName != tt.wantProvider || model != tt.wantModel {
				t.Fatalf("ParseStoredParts() = (%q, %q), want (%q, %q)", providerName, model, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

func TestSelectorHelpers(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		tests := []struct{ name, providerName, model, want string }{
			{name: "empty"},
			{name: "provider", providerName: " prov ", want: "prov/"},
			{name: "model", model: " model ", want: "model"},
			{name: "provider model", providerName: " prov ", model: " model ", want: "prov/model"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := String(tt.providerName, tt.model); got != tt.want {
					t.Fatalf("String() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("IsGlobal", func(t *testing.T) {
		tests := []struct {
			raw  string
			want bool
		}{{raw: "/", want: true}, {raw: " / ", want: true}, {raw: ""}, {raw: "prov/"}}
		for _, tt := range tests {
			if got := IsGlobal(tt.raw); got != tt.want {
				t.Fatalf("IsGlobal(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		}
	})

	t.Run("ScopeKindFor", func(t *testing.T) {
		tests := []struct {
			name         string
			selector     string
			providerName string
			model        string
			want         ScopeKind
		}{
			{name: "global", selector: "/", want: ScopeGlobal},
			{name: "provider model", providerName: "prov", model: "model", want: ScopeProviderModel},
			{name: "provider", providerName: "prov", want: ScopeProvider},
			{name: "model", model: "model", want: ScopeModel},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := ScopeKindFor(tt.selector, tt.providerName, tt.model); got != tt.want {
					t.Fatalf("ScopeKindFor() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("ExactMatchKey", func(t *testing.T) {
		tests := []struct{ name, providerName, model, want string }{
			{name: "empty"},
			{name: "provider only", providerName: "prov"},
			{name: "model only", model: "model"},
			{name: "provider model", providerName: " prov ", model: " model ", want: "prov/model"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := ExactMatchKey(tt.providerName, tt.model); got != tt.want {
					t.Fatalf("ExactMatchKey() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("splitFirst", func(t *testing.T) {
		tests := []struct {
			name       string
			value      string
			wantPrefix string
			wantRest   string
			wantOK     bool
		}{
			{name: "no slash", value: "model"},
			{name: "provider only", value: " prov / ", wantPrefix: "prov", wantOK: true},
			{name: "provider model", value: " prov / model ", wantPrefix: "prov", wantRest: "model", wantOK: true},
			{name: "first slash only", value: "org/model/variant", wantPrefix: "org", wantRest: "model/variant", wantOK: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				prefix, rest, ok := splitFirst(tt.value)
				if prefix != tt.wantPrefix || rest != tt.wantRest || ok != tt.wantOK {
					t.Fatalf("splitFirst() = (%q, %q, %v), want (%q, %q, %v)", prefix, rest, ok, tt.wantPrefix, tt.wantRest, tt.wantOK)
				}
			})
		}
	})
}

func assertNormalizeResult(t *testing.T, got Selector, err error, tt selectorCase) {
	t.Helper()
	if tt.wantErr {
		if err == nil {
			t.Fatal("normalize error = nil, want error")
		}
		if !IsValidationError(err) {
			t.Fatalf("normalize error = %T %v, want validation error", err, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("normalize error = %v", err)
	}
	if got != tt.want {
		t.Fatalf("selector = %+v, want %+v", got, tt.want)
	}
	if got.Selector != String(got.ProviderName, got.Model) && !IsGlobal(got.Selector) {
		t.Fatalf("selector string = %q, want canonical %q", got.Selector, String(got.ProviderName, got.Model))
	}
	if scope := ScopeKindFor(got.Selector, got.ProviderName, got.Model); scope != tt.wantScope {
		t.Fatalf("scope = %q, want %q", scope, tt.wantScope)
	}
	if key := ExactMatchKey(got.ProviderName, got.Model); key != tt.wantExactKey {
		t.Fatalf("exact key = %q, want %q", key, tt.wantExactKey)
	}
}
