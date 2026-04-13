package guardrails

import (
	"context"
	"testing"
)

func TestNewSystemPromptGuardrail_InvalidMode(t *testing.T) {
	_, err := NewSystemPromptGuardrail("test", "bad", "content")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestNewSystemPromptGuardrail_EmptyContent(t *testing.T) {
	_, err := NewSystemPromptGuardrail("test", SystemPromptInject, "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestNewSystemPromptGuardrail_ValidModes(t *testing.T) {
	for _, mode := range []SystemPromptMode{SystemPromptInject, SystemPromptOverride, SystemPromptDecorator} {
		g, err := NewSystemPromptGuardrail("my-guardrail", mode, "test")
		if err != nil {
			t.Fatalf("unexpected error for mode %q: %v", mode, err)
		}
		if g.Name() != "my-guardrail" {
			t.Errorf("expected name 'my-guardrail', got %q", g.Name())
		}
	}
}

func TestNewSystemPromptGuardrail_EmptyNameDefaults(t *testing.T) {
	g, err := NewSystemPromptGuardrail("", SystemPromptInject, "content")
	if err != nil {
		t.Fatal(err)
	}
	if g.Name() != "system_prompt" {
		t.Errorf("expected default name 'system_prompt', got %q", g.Name())
	}
}

func TestSystemPrompt_Inject_NoExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "injected system prompt")
	msgs := []Message{
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "injected system prompt" {
		t.Errorf("expected system message first, got %+v", result[0])
	}
	if result[1].Role != "user" {
		t.Errorf("expected user message second, got %+v", result[1])
	}
}

func TestSystemPrompt_Inject_ExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "injected system prompt")
	msgs := []Message{
		{Role: "system", Content: "original system"},
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (unchanged), got %d", len(result))
	}
	if result[0].Content != "original system" {
		t.Errorf("inject should not change existing system message, got %q", result[0].Content)
	}
}

func TestSystemPrompt_Override_NoExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "override prompt")
	msgs := []Message{
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "override prompt" {
		t.Errorf("expected override system message, got %+v", result[0])
	}
}

func TestSystemPrompt_Override_ExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "override prompt")
	msgs := []Message{
		{Role: "system", Content: "original system"},
		{Role: "user", Content: "hello"},
		{Role: "system", Content: "another system"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	// Should have: override system + user (both original system messages removed)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "override prompt" {
		t.Errorf("expected override system message, got %+v", result[0])
	}
	if result[1].Role != "user" {
		t.Errorf("expected user message, got %+v", result[1])
	}
}

func TestSystemPrompt_Decorator_NoExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptDecorator, "prefix")
	msgs := []Message{
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "prefix" {
		t.Errorf("decorator with no existing system should add one, got %+v", result[0])
	}
}

func TestSystemPrompt_Decorator_ExistingSystem(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptDecorator, "prefix")
	msgs := []Message{
		{Role: "system", Content: "original"},
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	expected := "prefix\noriginal"
	if result[0].Content != expected {
		t.Errorf("expected decorated content %q, got %q", expected, result[0].Content)
	}
}

func TestSystemPrompt_Decorator_OnlyFirstSystemDecorated(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptDecorator, "prefix")
	msgs := []Message{
		{Role: "system", Content: "first"},
		{Role: "user", Content: "hello"},
		{Role: "system", Content: "second"},
	}

	result, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}

	if result[0].Content != "prefix\nfirst" {
		t.Errorf("first system should be decorated, got %q", result[0].Content)
	}
	if result[2].Content != "second" {
		t.Errorf("second system should be untouched, got %q", result[2].Content)
	}
}

func TestSystemPrompt_DoesNotMutateOriginal(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "new")
	original := []Message{
		{Role: "system", Content: "original"},
		{Role: "user", Content: "hello"},
	}

	result, err := g.Process(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}

	// Original should be untouched
	if original[0].Content != "original" {
		t.Error("original messages were mutated")
	}
	if result[0].Content != "new" {
		t.Error("result should have new system message")
	}
}

func TestSystemPrompt_EmptyMessages(t *testing.T) {
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "injected")

	result, err := g.Process(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Content != "injected" {
		t.Errorf("expected injected message on empty input, got %v", result)
	}
}

func TestNewSystemPromptGuardrail_UnicodeNames(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"safety prompt"},         // spaces
		{"compliance check v2"},   // spaces and digits
		{"проверка безопасности"}, // Cyrillic with space
		{"安全検査"},                  // CJK (Chinese + Japanese)
		{"sécurité-modèle"},       // accented Latin
		{"🛡️ guardrail"},          // emoji
	}
	for _, tc := range tests {
		g, err := NewSystemPromptGuardrail(tc.name, SystemPromptInject, "content")
		if err != nil {
			t.Errorf("unexpected error for name %q: %v", tc.name, err)
			continue
		}
		if g.Name() != tc.name {
			t.Errorf("expected name %q, got %q", tc.name, g.Name())
		}
	}
}
