package tagging

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
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
			require.Equal(t, tt.want, got)

		})
	}
}

func TestNormalizeRules(t *testing.T) {
	rules := []Rule{
		{Header: "x-team "},
		{Header: "X-Cost-Center", Delimiter: ";"},
	}
	err := NormalizeRules(rules)
	require.NoError(t, err)
	require.Equal(t, "X-Team", rules[0].Header)
	require.Equal(t, DefaultDelimiter, rules[0].Delimiter, "rule not normalized: %#v", rules[0])
	require.Equal(t, ";", rules[1].Delimiter, "explicit delimiter overwritten: %#v", rules[1])

	for name, rules := range map[string][]Rule{
		"empty header":      {{Header: ""}},
		"invalid header":    {{Header: "bad header"}},
		"duplicate header":  {{Header: "X-Team"}, {Header: "x-team"}},
		"credential header": {{Header: "Authorization"}},
		"api key header":    {{Header: "x-api-key"}},
	} {
		err := NormalizeRules(rules)
		require.Error(t, err)
		require.True(t, IsValidationError(err), "%s: error %v must be a ValidationError", name, err)
	}
}

func TestStripHeaderSet(t *testing.T) {
	rules := []Rule{
		{Header: "X-Team", DoNotPass: true},
		{Header: "X-Keep"},
	}
	strip := StripHeaderSet(rules)
	_, ok := strip["X-Team"]
	require.True(t, ok, "X-Team missing from strip set: %#v", strip)
	_, ok = strip["X-Keep"]
	require.False(t, ok, "X-Keep should not be stripped: %#v", strip)
	require.Nil(t, StripHeaderSet(nil))
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
	err := service.Refresh(context.Background())
	require.NoError(t, err)

	rules := service.Rules()
	require.Len(t, rules, 2)
	require.Equal(t, "X-Team", rules[0].Header)
	require.Equal(t, "team-", rules[0].Prefix)
	require.True(t, rules[0].Managed, "config rule did not win: %#v", rules[0])
	require.Equal(t, "X-Env", rules[1].Header)
	require.False(t, rules[1].Managed, "store rule wrong: %#v", rules[1])

	labels := service.ExtractLabels(http.Header{"X-Team": {"team-alpha"}, "X-Env": {"prod"}})
	require.Equal(t, []string{"alpha", "prod"}, labels)
	_, ok := service.StripHeaders()["X-Team"]
	require.True(t, ok, "strip set missing X-Team: %#v", service.StripHeaders())
}

func TestServiceSaveRules(t *testing.T) {
	store := &fakeStore{}
	service := NewService([]Rule{{Header: "X-Managed"}}, store)

	merged, err := service.SaveRules(context.Background(), []Rule{{Header: "x-cost-center", Prefix: "cc-"}})
	require.NoError(t, err)
	require.Len(t, merged, 2)
	require.Equal(t, "X-Cost-Center", merged[1].Header)
	require.Len(t, store.rules, 1)
	require.Equal(t, "X-Cost-Center", store.rules[0].Header)
	_, err = service.SaveRules(context.Background(), []Rule{{Header: "X-Managed"}})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	_, err = service.SaveRules(context.Background(), []Rule{{Header: "bad header"}})
	require.Error(t, err)
	require.True(t, IsValidationError(err))

	unavailable := NewService(nil, nil)
	_, err = unavailable.SaveRules(context.Background(), []Rule{{Header: "X-A"}})
	require.Error(t, err)
	require.False(t, IsValidationError(err))
	require.False(t, unavailable.Editable())
}
