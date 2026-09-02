package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// initWorkflows builds guardrails, the workflows that reference them, and
// managed auth keys, then logs the startup summary. Guardrails must exist
// before the workflow compiler, and auth keys before the startup log, which
// reports the managed-key mode.
func (b *bootstrap) initWorkflows() error {
	app := b.app
	appCfg := b.appCfg
	vm := app.virtualModels.Service

	refreshInterval := workflowRefreshInterval(appCfg)
	var guardrailExecutor guardrails.ChatCompletionExecutor = app.providers.Router
	if vm != nil {
		guardrailExecutor = virtualmodels.NewChatExecutor(app.providers.Router, vm)
	}

	// Initialize reusable guardrail definitions using shared storage when already available.
	guardrailResult, err := guardrails.New(b.ctx, app.storage, refreshInterval, guardrailExecutor)
	if err != nil {
		return fmt.Errorf("failed to initialize guardrails: %w", err)
	}
	app.guardrails = guardrailResult
	app.register(subsystemGuardrails, ownedByShutdown, app.guardrails.Close)

	b.seedGuardrails, err = configGuardrailDefinitions(appCfg.Guardrails)
	if err != nil {
		return fmt.Errorf("failed to prepare guardrail definitions: %w", err)
	}
	if err := guardrailResult.Service.UpsertDefinitions(b.ctx, b.seedGuardrails); err != nil {
		return fmt.Errorf("failed to upsert guardrails: %w", err)
	}

	b.featureCaps = runtimeWorkflowFeatureCaps(appCfg)
	workflowCompiler := workflows.NewCompilerWithFeatureCaps(guardrailResult.Service, b.featureCaps)
	workflowResult, err := workflows.New(b.ctx, app.storage, workflowCompiler, refreshInterval)
	if err != nil {
		return fmt.Errorf("failed to initialize workflows: %w", err)
	}
	app.register(subsystemWorkflows, ownedByShutdown, workflowResult.Close)
	defaultWorkflow := defaultWorkflowInput(appCfg, guardrailResult.Service.Names(), b.seedGuardrails)
	if err := workflowResult.Service.EnsureDefaultGlobal(b.ctx, defaultWorkflow); err != nil {
		return fmt.Errorf("failed to seed workflows: %w", err)
	}
	if err := workflowResult.Service.Refresh(b.ctx); err != nil {
		return fmt.Errorf("failed to load workflows: %w", err)
	}
	app.workflows = workflowResult

	authKeyResult, err := authkeys.New(b.ctx, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize auth keys: %w", err)
	}
	app.authKeys = authKeyResult
	app.register(subsystemAuthKeys, ownedByShutdown, app.authKeys.Close)

	// Log configuration status after auth has been initialized so the startup
	// message reflects both bootstrap and managed auth modes.
	app.logStartupInfo()
	return nil
}

func configGuardrailDefinitions(cfg config.GuardrailsConfig) ([]guardrails.Definition, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	definitions := make([]guardrails.Definition, 0, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		name := strings.TrimSpace(rule.Name)
		ruleType := strings.ToLower(strings.TrimSpace(rule.Type))
		switch ruleType {
		case "llm-based-altering":
			ruleType = "llm_based_altering"
		}
		if name == "" {
			return nil, fmt.Errorf("guardrail rule #%d: name is required", i)
		}
		if ruleType == "" {
			return nil, fmt.Errorf("guardrail rule #%d (%q): type is required", i, name)
		}

		var rawConfig []byte
		var err error
		switch ruleType {
		case "system_prompt":
			rawConfig, err = json.Marshal(map[string]any{
				"mode":    rule.SystemPrompt.Mode,
				"content": rule.SystemPrompt.Content,
			})
		case "llm_based_altering":
			rawConfig, err = json.Marshal(map[string]any{
				"model":               rule.LLMBasedAltering.Model,
				"provider":            rule.LLMBasedAltering.Provider,
				"prompt":              rule.LLMBasedAltering.Prompt,
				"roles":               rule.LLMBasedAltering.Roles,
				"skip_content_prefix": rule.LLMBasedAltering.SkipContentPrefix,
				"max_tokens":          rule.LLMBasedAltering.MaxTokens,
			})
		default:
			return nil, fmt.Errorf("guardrail rule #%d (%q): unsupported type %q", i, name, ruleType)
		}
		if err != nil {
			return nil, fmt.Errorf("guardrail rule #%d (%q): marshal config: %w", i, name, err)
		}
		definitions = append(definitions, guardrails.Definition{
			Name:     name,
			Type:     ruleType,
			UserPath: strings.TrimSpace(rule.UserPath),
			Config:   rawConfig,
		})
	}
	return definitions, nil
}

func defaultWorkflowInput(cfg *config.Config, availableGuardrails []string, configuredGuardrails []guardrails.Definition) workflows.CreateInput {
	failoverEnabled := failoverFeatureEnabledGlobally(cfg)
	budgetEnabled := cfg.Budgets.Enabled
	payload := workflows.Payload{
		SchemaVersion: 1,
		Features: workflows.FeatureFlags{
			Cache:    responseCacheConfigured(cfg.Cache.Response),
			Audit:    cfg.Logging.Enabled,
			Usage:    cfg.Usage.Enabled,
			Budget:   &budgetEnabled,
			Failover: &failoverEnabled,
		},
	}
	available := make(map[string]struct{}, len(availableGuardrails))
	for _, name := range availableGuardrails {
		available[strings.TrimSpace(name)] = struct{}{}
	}
	for _, definition := range configuredGuardrails {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		available[name] = struct{}{}
	}
	if cfg.Guardrails.Enabled && len(cfg.Guardrails.Rules) > 0 {
		payload.Guardrails = make([]workflows.GuardrailStep, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				continue
			}
			if len(available) > 0 {
				if _, ok := available[name]; !ok {
					continue
				}
			}
			payload.Guardrails = append(payload.Guardrails, workflows.GuardrailStep{
				Ref:  name,
				Step: rule.Order,
			})
		}
	}
	payload.Features.Guardrails = len(payload.Guardrails) > 0

	return workflows.CreateInput{
		Scope:       workflows.Scope{},
		Activate:    true,
		Name:        workflows.ManagedDefaultGlobalName,
		Description: workflows.ManagedDefaultGlobalDescription,
		Payload:     payload,
	}
}

func runtimeWorkflowFeatureCaps(cfg *config.Config) core.WorkflowFeatures {
	if cfg == nil {
		return core.WorkflowFeatures{}
	}
	return core.WorkflowFeatures{
		Cache:      responseCacheConfigured(cfg.Cache.Response),
		Audit:      cfg.Logging.Enabled,
		Usage:      cfg.Usage.Enabled,
		Budget:     cfg.Budgets.Enabled,
		Guardrails: cfg.Guardrails.Enabled,
		Failover:   failoverFeatureEnabledGlobally(cfg),
	}
}

func workflowRefreshInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Workflows.RefreshInterval <= 0 {
		return time.Minute
	}
	return cfg.Workflows.RefreshInterval
}

func responseCacheConfigured(cfg config.ResponseCacheConfig) bool {
	return simpleResponseCacheConfiguredFromResponse(cfg) || semanticResponseCacheConfiguredFromResponse(cfg)
}

func simpleResponseCacheConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return simpleResponseCacheConfiguredFromResponse(cfg.Cache.Response)
}

func simpleResponseCacheConfiguredFromResponse(cfg config.ResponseCacheConfig) bool {
	return cfg.Simple != nil && config.SimpleCacheEnabled(cfg.Simple) &&
		cfg.Simple.Redis != nil && strings.TrimSpace(cfg.Simple.Redis.URL) != ""
}

func semanticResponseCacheConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return semanticResponseCacheConfiguredFromResponse(cfg.Cache.Response)
}

func semanticResponseCacheConfiguredFromResponse(cfg config.ResponseCacheConfig) bool {
	return cfg.Semantic != nil && config.SemanticCacheActive(cfg.Semantic)
}

func failoverFeatureEnabledGlobally(cfg *config.Config) bool {
	return cfg != nil && cfg.Failover.Enabled
}

// failoverResolver returns the virtual models service as the translated-route
// failover resolver: a redirect's remaining targets are its failover chain.
// FAILOVER_ENABLED=false switches the sweep off globally.
func failoverResolver(cfg *config.Config, vm *virtualmodels.Service) server.RequestFailoverResolver {
	if !failoverFeatureEnabledGlobally(cfg) || vm == nil {
		return nil
	}
	return vm
}
