package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/tagging"
)

type adminTaggingStore struct {
	rules   []tagging.Rule
	saveErr error
}

func (s *adminTaggingStore) GetRules(context.Context) ([]tagging.Rule, error) {
	return append([]tagging.Rule(nil), s.rules...), nil
}

func (s *adminTaggingStore) SaveRules(_ context.Context, rules []tagging.Rule) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.rules = rules
	return nil
}

func (s *adminTaggingStore) Close() error { return nil }

func newTaggingHandler(t *testing.T, configRules []tagging.Rule, store tagging.Store) *Handler {
	t.Helper()
	service := tagging.NewService(configRules, store)
	if store != nil {
		err := service.Refresh(context.Background())
		require.NoError(t, err)
	}
	return NewHandler(nil, nil, WithTagging(service))
}

func TestTaggingSettingsUnavailableWithoutService(t *testing.T) {
	h := NewHandler(nil, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		c, rec := echotest.Request(t, method, "/admin/tagging/settings", `{"headers":[]}`)
		var err error
		if method == http.MethodGet {
			err = h.TaggingSettings(c)
		} else {
			err = h.UpdateTaggingSettings(c)
		}
		require.NoError(t, err, method)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, method)
	}
}

func TestTaggingSettingsReturnsMergedView(t *testing.T) {
	store := &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Cost-Center", Prefix: "cc-"}}}
	h := newTaggingHandler(t, []tagging.Rule{{Header: "X-Team", DoNotPass: true}}, store)

	c, rec := echotest.Get(t, "/admin/tagging/settings")
	err := h.TaggingSettings(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[taggingSettingsResponse](t, rec)
	assert.True(t, body.Editable)
	require.Len(t, body.Headers, 2)
	assert.Equal(t, "X-Team", body.Headers[0].Header)
	assert.True(t, body.Headers[0].Managed)
	assert.Equal(t, "X-Cost-Center", body.Headers[1].Header)
	assert.False(t, body.Headers[1].Managed)
}

func TestUpdateTaggingSettingsReplacesOperatorRules(t *testing.T) {
	store := &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Old"}}}
	h := newTaggingHandler(t, nil, store)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/tagging/settings", `{"headers":[{"header":"x-cost-center","prefix":"cc-","do_not_pass":true}]}`)
	err := h.UpdateTaggingSettings(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := echotest.Decode[taggingSettingsResponse](t, rec)
	require.Len(t, body.Headers, 1)
	assert.Equal(t, "X-Cost-Center", body.Headers[0].Header)
	assert.True(t, body.Headers[0].DoNotPass)
	require.Len(t, store.rules, 1)
	assert.Equal(t, "X-Cost-Center", store.rules[0].Header)
}

func TestUpdateTaggingSettingsErrorClassification(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		saveErr    error
		wantStatus int
	}{
		{name: "malformed body", body: `{not json`, wantStatus: http.StatusBadRequest},
		{name: "invalid header name", body: `{"headers":[{"header":"bad header"}]}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate header", body: `{"headers":[{"header":"X-A"},{"header":"x-a"}]}`, wantStatus: http.StatusBadRequest},
		{name: "managed header is read-only", body: `{"headers":[{"header":"X-Team"}]}`, wantStatus: http.StatusBadRequest},
		{name: "credential header denied", body: `{"headers":[{"header":"Authorization"}]}`, wantStatus: http.StatusBadRequest},
		{
			// A storage error mentioning "read-only" must stay a 503, not be
			// mistaken for the managed-header validation error.
			name:       "storage failure",
			body:       `{"headers":[{"header":"X-Ok"}]}`,
			saveErr:    errors.New("cannot execute UPDATE in a read-only transaction"),
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &adminTaggingStore{saveErr: tc.saveErr}
			h := newTaggingHandler(t, []tagging.Rule{{Header: "X-Team"}}, store)

			c, rec := echotest.Request(t, http.MethodPut, "/admin/tagging/settings", tc.body)
			err := h.UpdateTaggingSettings(c)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
		})
	}
}
