package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gomodel/internal/admin"
	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type refreshableService interface {
	Refresh(context.Context) error
}

type runtimeRefreshStepResult struct {
	status  string
	message string
	err     error
}

// RefreshRuntime performs a manual runtime refresh for admin-triggered actions.
// It refreshes provider/model metadata and DB-backed in-memory snapshots without
// touching exact or semantic response caches.
func (a *App) RefreshRuntime(ctx context.Context) (admin.RuntimeRefreshReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	release, err := a.acquireRuntimeRefresh(ctx)
	if err != nil {
		return admin.RuntimeRefreshReport{}, err
	}
	defer release()

	startedAt := time.Now().UTC()
	report := admin.RuntimeRefreshReport{
		Status:    admin.RuntimeRefreshStatusOK,
		StartedAt: startedAt,
		Steps:     []admin.RuntimeRefreshStep{},
	}

	registry := a.modelRegistry()
	modelListURL := a.modelListURL()

	if err := a.runRuntimeRefreshStep(&report, "model_list", func() runtimeRefreshStepResult {
		if registry == nil {
			return runtimeRefreshStepResult{err: fmt.Errorf("model registry is unavailable")}
		}
		if modelListURL == "" {
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusSkipped,
				message: "model metadata URL is not configured",
			}
		}
		count, err := registry.RefreshModelList(ctx, modelListURL)
		if err != nil {
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusFailed,
				message: "kept previous model metadata",
				err:     err,
			}
		}
		return runtimeRefreshStepResult{
			message: fmt.Sprintf("downloaded %d model metadata entries", count),
		}
	}); err != nil {
		return report, err
	}

	if err := a.runRuntimeRefreshStep(&report, "providers", func() runtimeRefreshStepResult {
		if registry == nil {
			return runtimeRefreshStepResult{err: fmt.Errorf("model registry is unavailable")}
		}
		err := registry.Refresh(ctx)
		issueCount := providerRefreshIssueCount(registry.ProviderRuntimeSnapshots())
		switch {
		case err != nil && registry.ModelCount() > 0:
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusPartial,
				message: "previous provider model inventory is still available",
				err:     err,
			}
		case err != nil:
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusFailed,
				message: "no provider model inventory is available",
				err:     err,
			}
		case issueCount > 0:
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusPartial,
				message: fmt.Sprintf("%d provider refresh issue%s", issueCount, pluralSuffix(issueCount)),
			}
		default:
			return runtimeRefreshStepResult{
				message: fmt.Sprintf("refreshed %d provider model%s", registry.ModelCount(), pluralSuffix(registry.ModelCount())),
			}
		}
	}); err != nil {
		return report, err
	}

	if err := a.runRuntimeRefreshStep(&report, "model_registry_cache", func() runtimeRefreshStepResult {
		if registry == nil {
			return runtimeRefreshStepResult{err: fmt.Errorf("model registry is unavailable")}
		}
		if registry.ModelCount() == 0 {
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusSkipped,
				message: "no provider models are available to persist",
			}
		}
		if err := registry.SaveToCache(ctx); err != nil {
			return runtimeRefreshStepResult{err: err}
		}
		return runtimeRefreshStepResult{message: "persisted refreshed model registry"}
	}); err != nil {
		return report, err
	}

	if err := a.runRefreshableServiceStep(&report, "auth_keys", a.authKeyService(), ctx); err != nil {
		return report, err
	}
	if err := a.runRefreshableServiceStep(&report, "aliases", a.aliasService(), ctx); err != nil {
		return report, err
	}
	if err := a.runRefreshableServiceStep(&report, "model_overrides", a.modelOverrideService(), ctx); err != nil {
		return report, err
	}
	if err := a.runRefreshableServiceStep(&report, "guardrails", a.guardrailService(), ctx); err != nil {
		return report, err
	}
	if err := a.runRefreshableServiceStep(&report, "workflows", a.workflowService(), ctx); err != nil {
		return report, err
	}

	if registry != nil {
		report.ModelCount = registry.ModelCount()
		report.ProviderCount = registry.ProviderCount()
	}
	finalizeRuntimeRefreshReport(&report)
	return report, nil
}

func (a *App) runRefreshableServiceStep(report *admin.RuntimeRefreshReport, name string, service refreshableService, ctx context.Context) error {
	return a.runRuntimeRefreshStep(report, name, func() runtimeRefreshStepResult {
		if service == nil {
			return runtimeRefreshStepResult{
				status:  admin.RuntimeRefreshStatusSkipped,
				message: "service is not configured",
			}
		}
		if err := service.Refresh(ctx); err != nil {
			return runtimeRefreshStepResult{err: err}
		}
		return runtimeRefreshStepResult{message: "snapshot refreshed"}
	})
}

func (a *App) runRuntimeRefreshStep(report *admin.RuntimeRefreshReport, name string, fn func() runtimeRefreshStepResult) error {
	startedAt := time.Now()
	result := fn()
	if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
		return result.err
	}

	status := strings.TrimSpace(result.status)
	if status == "" {
		if result.err != nil {
			status = admin.RuntimeRefreshStatusFailed
		} else {
			status = admin.RuntimeRefreshStatusOK
		}
	}

	step := admin.RuntimeRefreshStep{
		Name:       name,
		Status:     status,
		Message:    strings.TrimSpace(result.message),
		DurationMS: time.Since(startedAt).Milliseconds(),
	}
	if result.err != nil {
		step.Error = result.err.Error()
	}
	report.Steps = append(report.Steps, step)
	return nil
}

func finalizeRuntimeRefreshReport(report *admin.RuntimeRefreshReport) {
	report.FinishedAt = time.Now().UTC()
	report.DurationMS = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	report.Status = aggregateRuntimeRefreshStatus(report.Steps)
	if report.ModelCount == 0 && runtimeRefreshStepStatus(report.Steps, "providers") == admin.RuntimeRefreshStatusFailed {
		report.Status = admin.RuntimeRefreshStatusFailed
	}
}

func aggregateRuntimeRefreshStatus(steps []admin.RuntimeRefreshStep) string {
	hasIssue := false
	hasSuccess := false
	for _, step := range steps {
		switch step.Status {
		case admin.RuntimeRefreshStatusFailed:
			hasIssue = true
		case admin.RuntimeRefreshStatusPartial:
			hasIssue = true
			hasSuccess = true
		case admin.RuntimeRefreshStatusOK:
			hasSuccess = true
		}
	}
	switch {
	case !hasIssue:
		return admin.RuntimeRefreshStatusOK
	case hasSuccess:
		return admin.RuntimeRefreshStatusPartial
	default:
		return admin.RuntimeRefreshStatusFailed
	}
}

func runtimeRefreshStepStatus(steps []admin.RuntimeRefreshStep, name string) string {
	for _, step := range steps {
		if step.Name == name {
			return step.Status
		}
	}
	return ""
}

func providerRefreshIssueCount(snapshots []providers.ProviderRuntimeSnapshot) int {
	var count int
	for _, snapshot := range snapshots {
		if strings.TrimSpace(snapshot.LastModelFetchError) != "" || strings.TrimSpace(snapshot.LastAvailabilityError) != "" {
			count++
		}
	}
	return count
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func (a *App) acquireRuntimeRefresh(ctx context.Context) (func(), error) {
	if a == nil {
		return nil, core.NewProviderError("runtime_refresh", http.StatusInternalServerError, "runtime refresh is unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, runtimeRefreshAcquireError(err)
	}
	ch := a.runtimeRefreshSemaphore()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, runtimeRefreshAcquireError(ctx.Err())
	}
}

func (a *App) runtimeRefreshSemaphore() chan struct{} {
	a.refreshOnce.Do(func() {
		if a.refreshCh == nil {
			a.refreshCh = make(chan struct{}, 1)
		}
	})
	return a.refreshCh
}

func runtimeRefreshAcquireError(err error) *core.GatewayError {
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewProviderError("runtime_refresh", http.StatusGatewayTimeout, "runtime refresh timed out before start", err)
	}
	return core.NewProviderError("runtime_refresh", http.StatusRequestTimeout, "runtime refresh canceled before start", err)
}

func (a *App) modelRegistry() *providers.ModelRegistry {
	if a == nil || a.providers == nil {
		return nil
	}
	return a.providers.Registry
}

func (a *App) modelListURL() string {
	if a == nil || a.config == nil {
		return ""
	}
	return strings.TrimSpace(a.config.Cache.Model.ModelList.URL)
}

func (a *App) authKeyService() refreshableService {
	if a == nil || a.authKeys == nil || a.authKeys.Service == nil {
		return nil
	}
	return a.authKeys.Service
}

func (a *App) aliasService() refreshableService {
	if a == nil || a.aliases == nil || a.aliases.Service == nil {
		return nil
	}
	return a.aliases.Service
}

func (a *App) modelOverrideService() refreshableService {
	if a == nil || a.modelOverrides == nil || a.modelOverrides.Service == nil {
		return nil
	}
	return a.modelOverrides.Service
}

func (a *App) guardrailService() refreshableService {
	if a == nil || a.guardrails == nil || a.guardrails.Service == nil {
		return nil
	}
	return a.guardrails.Service
}

func (a *App) workflowService() refreshableService {
	if a == nil || a.workflows == nil || a.workflows.Service == nil {
		return nil
	}
	return a.workflows.Service
}
