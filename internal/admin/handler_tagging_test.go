package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

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
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatalf("refresh tagging service: %v", err)
		}
	}
	return NewHandler(nil, nil, WithTagging(service))
}

func taggingContext(method, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/admin/tagging/settings", nil)
	} else {
		req = httptest.NewRequest(method, "/admin/tagging/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func decodeTaggingSettings(t *testing.T, rec *httptest.ResponseRecorder) taggingSettingsResponse {
	t.Helper()
	var body taggingSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return body
}

func TestTaggingSettingsUnavailableWithoutService(t *testing.T) {
	h := NewHandler(nil, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		c, rec := taggingContext(method, `{"headers":[]}`)
		var err error
		if method == http.MethodGet {
			err = h.TaggingSettings(c)
		} else {
			err = h.UpdateTaggingSettings(c)
		}
		if err != nil {
			t.Fatalf("%s handler failed: %v", method, err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want %d body=%s", method, rec.Code, http.StatusServiceUnavailable, rec.Body.String())
		}
	}
}

func TestTaggingSettingsReturnsMergedView(t *testing.T) {
	store := &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Cost-Center", Prefix: "cc-"}}}
	h := newTaggingHandler(t, []tagging.Rule{{Header: "X-Team", DoNotPass: true}}, store)

	c, rec := taggingContext(http.MethodGet, "")
	if err := h.TaggingSettings(c); err != nil {
		t.Fatalf("TaggingSettings() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeTaggingSettings(t, rec)
	if !body.Editable {
		t.Fatal("editable = false, want true")
	}
	if len(body.Headers) != 2 {
		t.Fatalf("headers len = %d, want 2: %#v", len(body.Headers), body.Headers)
	}
	if body.Headers[0].Header != "X-Team" || !body.Headers[0].Managed {
		t.Fatalf("managed rule wrong: %#v", body.Headers[0])
	}
	if body.Headers[1].Header != "X-Cost-Center" || body.Headers[1].Managed {
		t.Fatalf("operator rule wrong: %#v", body.Headers[1])
	}
}

func TestUpdateTaggingSettingsReplacesOperatorRules(t *testing.T) {
	store := &adminTaggingStore{rules: []tagging.Rule{{Header: "X-Old"}}}
	h := newTaggingHandler(t, nil, store)

	c, rec := taggingContext(http.MethodPut, `{"headers":[{"header":"x-cost-center","prefix":"cc-","do_not_pass":true}]}`)
	if err := h.UpdateTaggingSettings(c); err != nil {
		t.Fatalf("UpdateTaggingSettings() failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeTaggingSettings(t, rec)
	if len(body.Headers) != 1 || body.Headers[0].Header != "X-Cost-Center" || !body.Headers[0].DoNotPass {
		t.Fatalf("merged view wrong: %#v", body.Headers)
	}
	if len(store.rules) != 1 || store.rules[0].Header != "X-Cost-Center" {
		t.Fatalf("store not replaced: %#v", store.rules)
	}
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

			c, rec := taggingContext(http.MethodPut, tc.body)
			if err := h.UpdateTaggingSettings(c); err != nil {
				t.Fatalf("UpdateTaggingSettings() failed: %v", err)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
