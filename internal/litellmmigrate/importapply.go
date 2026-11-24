package litellmmigrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ApplyResult counts what an import wrote to GoModel.
type ApplyResult struct {
	Policies   int
	Budgets    int
	RateLimits int
	// Keys counts newly imported keys, KeysUpdated earlier imports brought up
	// to date (including deactivations).
	Keys        int
	KeysUpdated int
	// Failures describes each write GoModel rejected.
	Failures []string
}

// Apply writes plan to the GoModel admin API at baseURL, authenticating with
// adminKey. Policies, budgets, and rate limits go first, and a key whose
// path or an ancestor's rule was rejected is held back, so no key is ever
// less restricted than in LiteLLM. Every write is an upsert, so Apply can be
// re-run. It first checks that adminKey is a global admin credential and
// that GoModel has every feature the plan needs, and fails without writing
// anything if not; after that, a rejected write is recorded and the rest
// continue.
func Apply(ctx context.Context, client *http.Client, baseURL, adminKey string, plan *ImportPlan) (ApplyResult, error) {
	a := applier{ctx: ctx, client: client, base: strings.TrimRight(baseURL, "/") + "/admin", key: adminKey}
	var result ApplyResult
	if err := a.checkAccess(); err != nil {
		return result, err
	}
	if err := a.checkFeatures(plan); err != nil {
		return result, err
	}
	failedPaths := map[string]bool{}
	for _, policy := range plan.Policies {
		if a.send(http.MethodPut, "/users", policy, "policy "+policy.UserPath, &result) {
			result.Policies++
		} else {
			failedPaths[policy.UserPath] = true
		}
	}
	for _, budget := range plan.Budgets {
		body := map[string]any{
			"scope":      "user_path",
			"subject":    budget.UserPath,
			"amount":     budget.Amount,
			"budget_key": periodKey(budget.Period, budget.PeriodSeconds),
		}
		if a.send(http.MethodPut, "/budgets", body, "budget "+budget.UserPath, &result) {
			result.Budgets++
		} else {
			failedPaths[budget.UserPath] = true
		}
	}
	for _, limit := range plan.RateLimits {
		body := map[string]any{
			"scope":        "user_path",
			"subject":      limit.UserPath,
			"limit_key":    map[string]any{"period": limit.Period},
			"max_requests": limit.MaxRequests,
			"max_tokens":   limit.MaxTokens,
		}
		if a.send(http.MethodPut, "/rate-limits", body, "rate limit "+limit.UserPath, &result) {
			result.RateLimits++
		} else {
			failedPaths[limit.UserPath] = true
		}
	}
	for _, key := range plan.Keys {
		enabled := key.Enabled == nil || *key.Enabled
		if failed := failedAncestor(failedPaths, key.UserPath); enabled && failed != "" {
			result.Failures = append(result.Failures, fmt.Sprintf("key %s: not imported because a rule on %s was rejected", key.Name, failed))
			continue
		}
		status, err := a.do(http.MethodPost, "/auth-keys/import", key)
		switch {
		case err != nil:
			result.Failures = append(result.Failures, fmt.Sprintf("key %s: %v", key.Name, err))
		case status == http.StatusCreated:
			result.Keys++
		case status == http.StatusOK:
			result.KeysUpdated++
		}
	}
	return result, nil
}

// checkAccess confirms the admin key reaches GoModel with global scope, which
// the key import requires.
func (a applier) checkAccess() error {
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, a.base+"/access", nil)
	if err != nil {
		return err
	}
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("reach GoModel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GoModel admin API: %s", errorMessage(resp))
	}
	var access struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
		return fmt.Errorf("GoModel admin API: %w", err)
	}
	if access.Scope != "global" {
		return fmt.Errorf("the admin key is scoped to a user path; importing needs the master key or a global admin key")
	}
	return nil
}

// failedAncestor returns the path or the nearest ancestor of path whose rule
// write failed, or "".
func failedAncestor(failed map[string]bool, path string) string {
	for candidate := path; candidate != ""; candidate = candidate[:strings.LastIndex(candidate, "/")] {
		if failed[candidate] {
			return candidate
		}
	}
	return ""
}

// checkFeatures confirms GoModel serves every admin endpoint the plan writes
// to. A feature that is off, such as budgets without usage tracking, would
// otherwise reject every write of that kind on every run and hold the keys
// back.
func (a applier) checkFeatures(plan *ImportPlan) error {
	needed := []struct {
		name, path string
		count      int
	}{
		{"model policies", "/users", len(plan.Policies)},
		{"budgets", "/budgets", len(plan.Budgets)},
		{"rate limits", "/rate-limits", len(plan.RateLimits)},
		{"API keys", "/auth-keys", len(plan.Keys)},
	}
	var missing []string
	for _, feature := range needed {
		if feature.count == 0 {
			continue
		}
		if err := a.get(feature.path); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%d to import): %v", feature.name, feature.count, err))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("GoModel cannot take this import, so nothing was written; turn these features on and run again: %s", strings.Join(missing, "; "))
	}
	return nil
}

func (a applier) get(path string) error {
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return err
	}
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(errorMessage(resp))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func periodKey(period string, seconds int64) map[string]any {
	if period != "" {
		return map[string]any{"period": period}
	}
	return map[string]any{"period_seconds": seconds}
}

type applier struct {
	ctx    context.Context
	client *http.Client
	base   string
	key    string
}

func (a applier) send(method, path string, body any, what string, result *ApplyResult) bool {
	if _, err := a.do(method, path, body); err != nil {
		result.Failures = append(result.Failures, what+": "+err.Error())
		return false
	}
	return true
}

// do sends one admin request and returns its status, or an error for a
// rejected request.
func (a applier) do(method, path string, body any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(a.ctx, method, a.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("%s %s: %s", method, path, errorMessage(resp))
}

func errorMessage(resp *http.Response) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		return fmt.Sprintf("%d %s", resp.StatusCode, body.Error.Message)
	}
	return resp.Status
}
