package tagging

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestExtractLabels(t *testing.T) {
	tests := []struct {
		name    string
		rules   []Rule
		headers http.Header
		want    []string
	}{
		{
			name:    "no rules",
			headers: http.Header{"X-Team": {"alpha"}},
		},
		{
			name:  "single label",
			rules: []Rule{{Header: "X-Team", Delimiter: ","}},
			headers: http.Header{
				"X-Team": {"alpha"},
			},
			want: []string{"alpha"},
		},
		{
			name:  "default comma delimiter splits and trims",
			rules: []Rule{{Header: "X-Team"}},
			headers: http.Header{
				"X-Team": {" alpha , beta ,, "},
			},
			want: []string{"alpha", "beta"},
		},
		{
			name:  "custom delimiter",
			rules: []Rule{{Header: "X-Team", Delimiter: ";"}},
			headers: http.Header{
				"X-Team": {"alpha;beta,with-comma"},
			},
			want: []string{"alpha", "beta,with-comma"},
		},
		{
			name:  "prefix trimmed per label, missing prefix kept as-is",
			rules: []Rule{{Header: "X-Team", Prefix: "team-", Delimiter: ","}},
			headers: http.Header{
				"X-Team": {"team-alpha, beta, team-gamma"},
			},
			want: []string{"alpha", "beta", "gamma"},
		},
		{
			name: "multiple rules and repeated header values dedupe",
			rules: []Rule{
				{Header: "X-Team", Delimiter: ","},
				{Header: "X-Cost-Center", Prefix: "cc-", Delimiter: ","},
			},
			headers: http.Header{
				"X-Team":        {"alpha", "alpha,beta"},
				"X-Cost-Center": {"cc-alpha,cc-42"},
			},
			want: []string{"alpha", "beta", "42"},
		},
		{
			name:  "header absent",
			rules: []Rule{{Header: "X-Team", Delimiter: ","}},
			headers: http.Header{
				"X-Other": {"alpha"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLabels(tt.rules, tt.headers)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExtractLabels() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeRules(t *testing.T) {
	rules := []Rule{
		{Header: "x-team "},
		{Header: "X-Cost-Center", Delimiter: ";"},
	}
	if err := NormalizeRules(rules); err != nil {
		t.Fatalf("NormalizeRules() error = %v", err)
	}
	if rules[0].Header != "X-Team" || rules[0].Delimiter != DefaultDelimiter {
		t.Fatalf("rule not normalized: %#v", rules[0])
	}
	if rules[1].Delimiter != ";" {
		t.Fatalf("explicit delimiter overwritten: %#v", rules[1])
	}

	for name, rules := range map[string][]Rule{
		"empty header":      {{Header: ""}},
		"invalid header":    {{Header: "bad header"}},
		"duplicate header":  {{Header: "X-Team"}, {Header: "x-team"}},
		"credential header": {{Header: "Authorization"}},
		"api key header":    {{Header: "x-api-key"}},
	} {
		err := NormalizeRules(rules)
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if !IsValidationError(err) {
			t.Fatalf("%s: error %v must be a ValidationError", name, err)
		}
	}
}

func TestStripHeaderSet(t *testing.T) {
	rules := []Rule{
		{Header: "X-Team", DoNotPass: true},
		{Header: "X-Keep"},
	}
	strip := StripHeaderSet(rules)
	if _, ok := strip["X-Team"]; !ok {
		t.Fatalf("X-Team missing from strip set: %#v", strip)
	}
	if _, ok := strip["X-Keep"]; ok {
		t.Fatalf("X-Keep should not be stripped: %#v", strip)
	}
	if StripHeaderSet(nil) != nil {
		t.Fatal("empty rules should produce nil strip set")
	}
}

type fakeStore struct {
	rules []Rule
}

func (f *fakeStore) GetRules(_ context.Context) ([]Rule, error) { return f.rules, nil }
func (f *fakeStore) SaveRules(_ context.Context, rules []Rule) error {
	f.rules = rules
	return nil
}
func (f *fakeStore) Close() error { return nil }

func TestServiceMergesConfigOverStore(t *testing.T) {
	store := &fakeStore{rules: []Rule{
		{Header: "X-Team", Prefix: "stored-"}, // shadowed by config
		{Header: "X-Env"},
	}}
	service := NewService([]Rule{{Header: "X-Team", Prefix: "team-", DoNotPass: true}}, store)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	rules := service.Rules()
	if len(rules) != 2 {
		t.Fatalf("rules len = %d, want 2: %#v", len(rules), rules)
	}
	if rules[0].Header != "X-Team" || rules[0].Prefix != "team-" || !rules[0].Managed {
		t.Fatalf("config rule did not win: %#v", rules[0])
	}
	if rules[1].Header != "X-Env" || rules[1].Managed {
		t.Fatalf("store rule wrong: %#v", rules[1])
	}

	labels := service.ExtractLabels(http.Header{"X-Team": {"team-alpha"}, "X-Env": {"prod"}})
	if !reflect.DeepEqual(labels, []string{"alpha", "prod"}) {
		t.Fatalf("ExtractLabels() = %#v", labels)
	}
	if _, ok := service.StripHeaders()["X-Team"]; !ok {
		t.Fatalf("strip set missing X-Team: %#v", service.StripHeaders())
	}
}

func TestServiceSaveRules(t *testing.T) {
	store := &fakeStore{}
	service := NewService([]Rule{{Header: "X-Managed"}}, store)

	merged, err := service.SaveRules(context.Background(), []Rule{{Header: "x-cost-center", Prefix: "cc-"}})
	if err != nil {
		t.Fatalf("SaveRules() error = %v", err)
	}
	if len(merged) != 2 || merged[1].Header != "X-Cost-Center" {
		t.Fatalf("merged view wrong: %#v", merged)
	}
	if len(store.rules) != 1 || store.rules[0].Header != "X-Cost-Center" {
		t.Fatalf("store not updated: %#v", store.rules)
	}

	if _, err := service.SaveRules(context.Background(), []Rule{{Header: "X-Managed"}}); err == nil || !IsValidationError(err) {
		t.Fatalf("managed header: err = %v, want ValidationError", err)
	}
	if _, err := service.SaveRules(context.Background(), []Rule{{Header: "bad header"}}); err == nil || !IsValidationError(err) {
		t.Fatalf("invalid header: err = %v, want ValidationError", err)
	}

	unavailable := NewService(nil, nil)
	if _, err := unavailable.SaveRules(context.Background(), []Rule{{Header: "X-A"}}); err == nil || IsValidationError(err) {
		t.Fatalf("storage-unavailable: err = %v, want non-validation error", err)
	}
	if unavailable.Editable() {
		t.Fatal("service without store must not be editable")
	}
}
