package admin

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// workflowsJSPath holds the dashboard module that mirrors DashboardConfigResponse.
const workflowsJSPath = "dashboard/static/js/modules/workflows.js"

var (
	allowlistBlockRe = regexp.MustCompile(`(?s)workflowRuntimeConfigKeys\(\)\s*\{\s*return\s*\[(.*?)\]`)
	allowlistKeyRe   = regexp.MustCompile(`'([A-Z0-9_]+)'`)
)

// TestDashboardConfigContract_MatchesFrontendAllowlist pins the runtime-config
// contract to the dashboard's client-side allowlist.
//
// fetchWorkflowRuntimeConfig() copies only allowlisted keys out of the
// /admin/runtime/config payload, so a flag the backend emits but the JS omits
// is silently dropped and its feature gate falls back to its default. That is
// how RATE_LIMITS_ENABLED once left the Rate Limits nav item visible with the
// feature switched off. Keep both lists in lockstep.
func TestDashboardConfigContract_MatchesFrontendAllowlist(t *testing.T) {
	backend := map[string]bool{}
	rt := reflect.TypeFor[DashboardConfigResponse]()
	for field := range rt.Fields() {
		tag := field.Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			backend[name] = true
		}
	}
	if len(backend) == 0 {
		t.Fatal("DashboardConfigResponse exposes no json-tagged fields")
	}

	source, err := os.ReadFile(workflowsJSPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowsJSPath, err)
	}
	block := allowlistBlockRe.FindSubmatch(source)
	if block == nil {
		t.Fatalf("workflowRuntimeConfigKeys() allowlist not found in %s", workflowsJSPath)
	}

	frontend := map[string]bool{}
	for _, m := range allowlistKeyRe.FindAllSubmatch(block[1], -1) {
		frontend[string(m[1])] = true
	}

	for key := range backend {
		if !frontend[key] {
			t.Errorf("%s is served by /admin/runtime/config but missing from workflowRuntimeConfigKeys() in %s; the dashboard will drop it and fall back to the gate's default", key, workflowsJSPath)
		}
	}
	for key := range frontend {
		if !backend[key] {
			t.Errorf("workflowRuntimeConfigKeys() allowlists %s, but DashboardConfigResponse never emits it", key)
		}
	}
}
