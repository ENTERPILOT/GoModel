package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func TestEnrichEntryWithWorkflow_PrefersProviderNameForResolvedModel(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	entry := &LogEntry{ID: "provider-name-prefill"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{
				Provider: "openai",
				Model:    "gpt-5-nano",
			},
			ProviderName: "openai_test",
		},
	})

	if got := entry.Provider; got != "openai" {
		t.Fatalf("Provider = %q, want %q", got, "openai")
	}
	if got := entry.ProviderName; got != "openai_test" {
		t.Fatalf("ProviderName = %q, want %q", got, "openai_test")
	}
	if got := entry.ResolvedModel; got != "openai_test/gpt-5-nano" {
		t.Fatalf("ResolvedModel = %q, want %q", got, "openai_test/gpt-5-nano")
	}
}
