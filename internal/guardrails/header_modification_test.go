package guardrails

import (
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

func mustHeaderModification(t *testing.T, config string) *HeaderModificationGuardrail {
	t.Helper()
	cfg, err := decodeHeaderModificationDefinitionConfig(json.RawMessage(config))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	guardrail, err := NewHeaderModificationGuardrail("test-rule", cfg)
	if err != nil {
		t.Fatalf("build guardrail: %v", err)
	}
	return guardrail
}

func TestHeaderModificationConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:   "valid set and remove",
			config: `{"when":[{"header":"User-Agent","matches":"^cline/"}],"actions":[{"action":"set","header":"X-Env","value":"prod"},{"action":"remove","header":"X-Debug"}]}`,
		},
		{
			name:   "valid unconditional",
			config: `{"actions":[{"action":"set","header":"X-Env","value":"prod"}]}`,
		},
		{
			name:   "valid from_header copy",
			config: `{"actions":[{"action":"set","header":"X-Copy","from_header":"X-Origin"}]}`,
		},
		{
			name:    "no actions",
			config:  `{"when":[{"header":"X-A"}]}`,
			wantErr: "at least one action",
		},
		{
			name:    "credential action target",
			config:  `{"actions":[{"action":"set","header":"Authorization","value":"Bearer x"}]}`,
			wantErr: "credentials",
		},
		{
			name:    "credential condition source",
			config:  `{"when":[{"header":"X-Api-Key"}],"actions":[{"action":"remove","header":"X-Debug"}]}`,
			wantErr: "credentials",
		},
		{
			name:    "transport header target",
			config:  `{"actions":[{"action":"set","header":"Content-Length","value":"0"}]}`,
			wantErr: "transport",
		},
		{
			name:    "content type target",
			config:  `{"actions":[{"action":"set","header":"Content-Type","value":"text/plain"}]}`,
			wantErr: "payload encoding or media type",
		},
		{
			name:    "content encoding source",
			config:  `{"when":[{"header":"Content-Encoding"}],"actions":[{"action":"remove","header":"X-Debug"}]}`,
			wantErr: "payload encoding or media type",
		},
		{
			name:    "accept encoding target",
			config:  `{"actions":[{"action":"remove","header":"Accept-Encoding"}]}`,
			wantErr: "payload encoding or media type",
		},
		{
			name:    "credential from_header source",
			config:  `{"actions":[{"action":"set","header":"X-Copy","from_header":"Cookie"}]}`,
			wantErr: "credentials",
		},
		{
			name:    "invalid regex",
			config:  `{"when":[{"header":"X-A","matches":"("}],"actions":[{"action":"remove","header":"X-B"}]}`,
			wantErr: "regex",
		},
		{
			name:    "equals and matches together",
			config:  `{"when":[{"header":"X-A","equals":"x","matches":"y"}],"actions":[{"action":"remove","header":"X-B"}]}`,
			wantErr: "mutually exclusive",
		},
		{
			name:    "set without value",
			config:  `{"actions":[{"action":"set","header":"X-A"}]}`,
			wantErr: "requires value or from_header",
		},
		{
			name:    "set with value and from_header",
			config:  `{"actions":[{"action":"set","header":"X-A","value":"x","from_header":"X-B"}]}`,
			wantErr: "mutually exclusive",
		},
		{
			name:    "remove with value",
			config:  `{"actions":[{"action":"remove","header":"X-A","value":"x"}]}`,
			wantErr: "no value",
		},
		{
			name:    "unknown action",
			config:  `{"actions":[{"action":"append","header":"X-A","value":"x"}]}`,
			wantErr: "unknown action",
		},
		{
			name:    "invalid header name",
			config:  `{"actions":[{"action":"remove","header":"bad header"}]}`,
			wantErr: "invalid header name",
		},
		{
			name:    "present false with equals",
			config:  `{"when":[{"header":"X-A","equals":"x","present":false}],"actions":[{"action":"remove","header":"X-B"}]}`,
			wantErr: "present=false",
		},
		{
			name:    "unknown config key",
			config:  `{"actions":[{"action":"remove","header":"X-A"}],"bogus":true}`,
			wantErr: "invalid header_modification config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeHeaderModificationDefinitionConfig(json.RawMessage(tt.config))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestHeaderModificationEvaluation(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		inbound     http.Header
		wantNil     bool
		wantSet     map[string]string
		wantRemoved []string
	}{
		{
			name:    "unconditional set",
			config:  `{"actions":[{"action":"set","header":"anthropic-beta","value":"context-1m"}]}`,
			inbound: http.Header{},
			wantSet: map[string]string{"Anthropic-Beta": "context-1m"},
		},
		{
			name:    "regex condition matches",
			config:  `{"when":[{"header":"User-Agent","matches":"^cline/"}],"actions":[{"action":"set","header":"X-Env","value":"agent"}]}`,
			inbound: http.Header{"User-Agent": []string{"cline/3.2"}},
			wantSet: map[string]string{"X-Env": "agent"},
		},
		{
			name:    "regex condition does not match",
			config:  `{"when":[{"header":"User-Agent","matches":"^cline/"}],"actions":[{"action":"set","header":"X-Env","value":"agent"}]}`,
			inbound: http.Header{"User-Agent": []string{"curl/8"}},
			wantNil: true,
		},
		{
			name:    "equals matches any value",
			config:  `{"when":[{"header":"X-Mode","equals":"fast"}],"actions":[{"action":"remove","header":"X-Slow"}]}`,
			inbound: http.Header{"X-Mode": []string{"slow", "fast"}},
			wantRemoved: []string{
				"X-Slow",
			},
		},
		{
			name:    "equals empty matches an explicitly empty value",
			config:  `{"when":[{"header":"X-Mode","equals":""}],"actions":[{"action":"set","header":"X-Empty","value":"matched"}]}`,
			inbound: http.Header{"X-Mode": []string{""}},
			wantSet: map[string]string{"X-Empty": "matched"},
		},
		{
			name:    "equals empty does not degrade to present",
			config:  `{"when":[{"header":"X-Mode","equals":""}],"actions":[{"action":"set","header":"X-Empty","value":"matched"}]}`,
			inbound: http.Header{"X-Mode": []string{"non-empty"}},
			wantNil: true,
		},
		{
			name:    "present condition",
			config:  `{"when":[{"header":"X-Trace"}],"actions":[{"action":"set","header":"X-Traced","value":"1"}]}`,
			inbound: http.Header{"X-Trace": []string{"abc"}},
			wantSet: map[string]string{"X-Traced": "1"},
		},
		{
			name:    "absent condition",
			config:  `{"when":[{"header":"X-Version","present":false}],"actions":[{"action":"set","header":"X-Version","value":"default"}]}`,
			inbound: http.Header{},
			wantSet: map[string]string{"X-Version": "default"},
		},
		{
			name:    "absent condition fails when header exists",
			config:  `{"when":[{"header":"X-Version","present":false}],"actions":[{"action":"set","header":"X-Version","value":"default"}]}`,
			inbound: http.Header{"X-Version": []string{"7"}},
			wantNil: true,
		},
		{
			name:    "all conditions must hold",
			config:  `{"when":[{"header":"X-A"},{"header":"X-B"}],"actions":[{"action":"set","header":"X-C","value":"1"}]}`,
			inbound: http.Header{"X-A": []string{"1"}},
			wantNil: true,
		},
		{
			name:    "from_header copies inbound value",
			config:  `{"actions":[{"action":"set","header":"X-Team","from_header":"X-Client-Team"}]}`,
			inbound: http.Header{"X-Client-Team": []string{"platform"}},
			wantSet: map[string]string{"X-Team": "platform"},
		},
		{
			name:    "from_header skips when source absent",
			config:  `{"actions":[{"action":"set","header":"X-Team","from_header":"X-Client-Team"}]}`,
			inbound: http.Header{},
			wantNil: true,
		},
		{
			name:        "later action wins over earlier",
			config:      `{"actions":[{"action":"set","header":"X-A","value":"1"},{"action":"remove","header":"X-A"}]}`,
			inbound:     http.Header{},
			wantRemoved: []string{"X-A"},
		},
		{
			name:    "case-insensitive condition header",
			config:  `{"when":[{"header":"x-trace"}],"actions":[{"action":"set","header":"X-Traced","value":"1"}]}`,
			inbound: http.Header{"X-Trace": []string{"abc"}},
			wantSet: map[string]string{"X-Traced": "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guardrail := mustHeaderModification(t, tt.config)
			mutation := guardrail.HeaderMutation(tt.inbound)
			if tt.wantNil {
				if !mutation.IsZero() {
					t.Fatalf("expected no mutation, got %+v", mutation)
				}
				return
			}
			if mutation.IsZero() {
				t.Fatal("expected mutation, got none")
			}
			if len(mutation.Set) != len(tt.wantSet) {
				t.Fatalf("set mismatch: want %v, got %v", tt.wantSet, mutation.Set)
			}
			for name, value := range tt.wantSet {
				if mutation.Set[name] != value {
					t.Fatalf("set[%s]: want %q, got %q", name, value, mutation.Set[name])
				}
			}
			if len(mutation.Remove) != len(tt.wantRemoved) {
				t.Fatalf("removed mismatch: want %v, got %v", tt.wantRemoved, mutation.Remove)
			}
			for i, name := range tt.wantRemoved {
				if mutation.Remove[i] != name {
					t.Fatalf("removed[%d]: want %q, got %q", i, name, mutation.Remove[i])
				}
			}
		})
	}
}

func TestHeaderModificationProcessIsIdentity(t *testing.T) {
	guardrail := mustHeaderModification(t, `{"actions":[{"action":"set","header":"X-A","value":"1"}]}`)
	msgs := []Message{{Role: "user", Content: "hi"}}
	got, err := guardrail.Process(t.Context(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" || got[0].Content != "hi" {
		t.Fatalf("expected identity, got %+v", got)
	}
}

func TestPipelineHeaderMutatorsOrder(t *testing.T) {
	first := mustHeaderModification(t, `{"actions":[{"action":"set","header":"X-A","value":"1"}]}`)
	second := mustHeaderModification(t, `{"actions":[{"action":"set","header":"X-B","value":"2"}]}`)
	prompt, err := NewSystemPromptGuardrail("sp", SystemPromptInject, "hello")
	if err != nil {
		t.Fatalf("build system prompt: %v", err)
	}

	pipeline := NewPipeline()
	pipeline.Add(second, 2)
	pipeline.Add(prompt, 1)
	pipeline.Add(first, 0)

	mutators := pipeline.HeaderMutators()
	if len(mutators) != 2 {
		t.Fatalf("expected 2 header mutators, got %d", len(mutators))
	}
	if mutators[0].Name() != first.Name() || mutators[1].Name() != second.Name() {
		t.Fatalf("unexpected order: %s, %s", mutators[0].Name(), mutators[1].Name())
	}
}

func TestHeaderModificationDefinitionNormalization(t *testing.T) {
	def := Definition{
		Name:   "beta-pin",
		Type:   "header-modification",
		Config: json.RawMessage(`{"actions":[{"action":"SET","header":"anthropic-beta","value":"context-1m"}]}`),
	}
	normalized, err := normalizeDefinition(def)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if normalized.Type != "header_modification" {
		t.Fatalf("type alias not normalized: %q", normalized.Type)
	}
	var cfg headerModificationDefinitionConfig
	if err := json.Unmarshal(normalized.Config, &cfg); err != nil {
		t.Fatalf("unmarshal normalized config: %v", err)
	}
	if cfg.Actions[0].Action != "set" || cfg.Actions[0].Header != "Anthropic-Beta" {
		t.Fatalf("action not canonicalized: %+v", cfg.Actions[0])
	}

	view := ViewFromDefinition(normalized)
	if !strings.Contains(view.Summary, "set Anthropic-Beta") {
		t.Fatalf("unexpected summary: %q", view.Summary)
	}
}
