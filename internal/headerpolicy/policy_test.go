package headerpolicy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestPolicyResolveHeaderPlan(t *testing.T) {
	policy, err := NewPolicy(Definition{
		Name: "pin-beta", Methods: []string{"post"}, Paths: []string{"/v1/*"},
		When: []Condition{{Header: "X-Client", Equals: new("")}},
		Actions: []Action{
			{Action: ActionSet, Header: "anthropic-beta", Value: new("context-1m")},
			{Action: ActionSet, Header: "X-Team", FromHeader: "X-Client-Team"},
			{Action: ActionRemove, Header: "X-Debug"},
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	plan := policy.ResolveHeaderPlan(core.HeaderPolicyInput{
		Method: "POST", Path: "/v1/chat/completions",
		Headers: http.Header{"X-Client": {""}, "X-Client-Team": {"platform"}},
	})
	if plan == nil {
		t.Fatal("ResolveHeaderPlan() = nil")
	}
	if plan.Set["Anthropic-Beta"] != "context-1m" || plan.Set["X-Team"] != "platform" {
		t.Fatalf("plan.Set = %#v", plan.Set)
	}
	if len(plan.Remove) != 1 || plan.Remove[0] != "X-Debug" {
		t.Fatalf("plan.Remove = %#v", plan.Remove)
	}
}

func TestPolicyCopiesPresentEmptyHeaderValue(t *testing.T) {
	policy, err := NewPolicy(Definition{
		Name:    "copy-empty",
		Actions: []Action{{Action: ActionSet, Header: "X-Upstream-Value", FromHeader: "X-Client-Value"}},
	})
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	plan := policy.ResolveHeaderPlan(core.HeaderPolicyInput{Headers: http.Header{"X-Client-Value": {""}}})
	if plan == nil {
		t.Fatal("ResolveHeaderPlan() = nil, want an explicit empty set value")
	}
	value, exists := plan.Set["X-Upstream-Value"]
	if !exists || value != "" {
		t.Fatalf("plan.Set = %#v, want X-Upstream-Value with empty value", plan.Set)
	}
}

func TestPolicyMarksCopiedCredentialLikeSourceSensitive(t *testing.T) {
	for _, source := range []string{"X-Session-Token", "X-ApiKey", "X-ClientSecret"} {
		t.Run(source, func(t *testing.T) {
			policy, err := NewPolicy(Definition{
				Name:    "copy-token",
				Actions: []Action{{Action: ActionSet, Header: "X-Team", FromHeader: source}},
			})
			if err != nil {
				t.Fatalf("NewPolicy() error = %v", err)
			}
			headers := make(http.Header)
			headers.Set(source, "secret")
			plan := policy.ResolveHeaderPlan(core.HeaderPolicyInput{Headers: headers})
			if plan == nil || plan.Set["X-Team"] != "secret" {
				t.Fatalf("plan = %#v", plan)
			}
			if len(plan.SensitiveSet) != 1 || plan.SensitiveSet[0] != "X-Team" {
				t.Fatalf("plan.SensitiveSet = %v", plan.SensitiveSet)
			}
		})
	}
}

func TestPolicyMarksCredentialLikeDestinationSensitive(t *testing.T) {
	for _, action := range []Action{
		{Action: ActionSet, Header: "X-Session-Token", Value: new("literal-secret")},
		{Action: ActionSet, Header: "X-Session-Token", FromHeader: "X-Team"},
	} {
		policy, err := NewPolicy(Definition{Name: "credential-destination", Actions: []Action{action}})
		if err != nil {
			t.Fatalf("NewPolicy() error = %v", err)
		}
		plan := policy.ResolveHeaderPlan(core.HeaderPolicyInput{Headers: http.Header{"X-Team": {"platform"}}})
		if plan == nil {
			t.Fatal("ResolveHeaderPlan() = nil")
		}
		if len(plan.SensitiveSet) != 1 || plan.SensitiveSet[0] != "X-Session-Token" {
			t.Fatalf("plan.SensitiveSet = %v", plan.SensitiveSet)
		}
	}
}

func TestPolicyValidationRejectsProtectedAndPayloadHeaders(t *testing.T) {
	for _, header := range []string{"Authorization", "Content-Type", "Accept-Encoding"} {
		t.Run(header, func(t *testing.T) {
			_, err := NewPolicy(Definition{Name: "unsafe", Actions: []Action{{Action: ActionRemove, Header: header}}})
			if err == nil {
				t.Fatalf("NewPolicy() accepted %s", header)
			}
		})
	}
}

func TestDefinitionFromLegacyMapsEndpointsToPaths(t *testing.T) {
	definition, err := DefinitionFromLegacy("legacy", "", []byte(`{
		"methods":["post"],
		"endpoints":["/v1/*"],
		"actions":[{"action":"set","header":"X-Test","value":""}]
	}`))
	if err != nil {
		t.Fatalf("DefinitionFromLegacy() error = %v", err)
	}
	if len(definition.Paths) != 1 || definition.Paths[0] != "/v1/*" {
		t.Fatalf("Paths = %#v", definition.Paths)
	}
	if definition.Actions[0].Value == nil || *definition.Actions[0].Value != "" {
		t.Fatalf("empty literal value was not preserved: %#v", definition.Actions[0])
	}
}

func TestDefinitionFromLegacyRejectsTrailingJSON(t *testing.T) {
	_, err := DefinitionFromLegacy("legacy", "", []byte(`{"actions":[{"action":"remove","header":"X-Test"}]} {}`))
	if err == nil {
		t.Fatal("DefinitionFromLegacy() error = nil, want trailing JSON error")
	}
}

type memoryStore struct {
	definitions           map[string]Definition
	failList              bool
	failListAfterMutation bool
}

func (s *memoryStore) List(context.Context) ([]Definition, error) {
	if s.failList {
		return nil, errors.New("list failed")
	}
	result := make([]Definition, 0, len(s.definitions))
	for _, definition := range s.definitions {
		result = append(result, definition)
	}
	return result, nil
}
func (s *memoryStore) Get(_ context.Context, name string) (*Definition, error) {
	definition, ok := s.definitions[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &definition, nil
}
func (s *memoryStore) Upsert(_ context.Context, definition Definition) error {
	s.definitions[definition.Name] = definition
	if s.failListAfterMutation {
		s.failList = true
	}
	return nil
}
func (s *memoryStore) UpsertMany(_ context.Context, definitions []Definition) error {
	for _, definition := range definitions {
		s.definitions[definition.Name] = definition
	}
	if s.failListAfterMutation {
		s.failList = true
	}
	return nil
}
func (s *memoryStore) Delete(_ context.Context, name string) error {
	if _, ok := s.definitions[name]; !ok {
		return ErrNotFound
	}
	delete(s.definitions, name)
	if s.failListAfterMutation {
		s.failList = true
	}
	return nil
}
func (s *memoryStore) Close() error { return nil }

func TestServiceBuildHeaderPolicies(t *testing.T) {
	store := &memoryStore{definitions: map[string]Definition{}}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Upsert(context.Background(), Definition{
		Name: "headers", Actions: []Action{{Action: ActionSet, Header: "X-Test", Value: new("1")}},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	policies, err := service.BuildHeaderPolicies([]Reference{{Ref: "headers", Step: 10}})
	if err != nil {
		t.Fatalf("BuildHeaderPolicies() error = %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("policies = %#v", policies)
	}
}

func TestServicePublishesCommittedMutationsWithoutPostWriteRefresh(t *testing.T) {
	definition := Definition{Name: "headers", Actions: []Action{{Action: ActionRemove, Header: "X-Debug"}}}
	tests := []struct {
		name   string
		seed   map[string]Definition
		mutate func(context.Context, *Service) error
		want   bool
	}{
		{
			name: "upsert", seed: map[string]Definition{}, want: true,
			mutate: func(ctx context.Context, service *Service) error { return service.Upsert(ctx, definition) },
		},
		{
			name: "upsert many", seed: map[string]Definition{}, want: true,
			mutate: func(ctx context.Context, service *Service) error {
				return service.UpsertDefinitions(ctx, []Definition{definition})
			},
		},
		{
			name: "delete", seed: map[string]Definition{"headers": definition}, want: false,
			mutate: func(ctx context.Context, service *Service) error { return service.Delete(ctx, "headers") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memoryStore{definitions: tt.seed}
			service, err := NewService(store)
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			if err := service.Refresh(t.Context()); err != nil {
				t.Fatalf("initial Refresh() error = %v", err)
			}
			store.failListAfterMutation = true
			if err := tt.mutate(t.Context(), service); err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			if _, ok := service.Get("headers"); ok != tt.want {
				t.Fatalf("catalog contains headers = %v, want %v", ok, tt.want)
			}
			if err := service.Refresh(t.Context()); err == nil {
				t.Fatal("Refresh() error = nil, want simulated list failure")
			}
			if _, ok := service.Get("headers"); ok != tt.want {
				t.Fatalf("failed refresh changed catalog: contains headers = %v, want %v", ok, tt.want)
			}
		})
	}
}
