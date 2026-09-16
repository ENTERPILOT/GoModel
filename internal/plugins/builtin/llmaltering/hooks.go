package llmaltering

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/pluginapi"
)

// OnPrompt rewrites the text parts of every message whose role is selected,
// tool-result text included.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	var targets []pluginapi.TextTarget
	for _, t := range x.Prompt.TextTargets() {
		if _, ok := p.roles[t.Role]; ok && p.shouldRewrite(t.Text) {
			targets = append(targets, t)
		}
	}
	return pluginapi.Allow(), p.rewriteTargets(ctx, targets, x.Prompt.SetTargetText)
}

// OnResponse rewrites the assistant text of every choice when the assistant
// role is selected.
func (p *Plugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x == nil || x.Response == nil {
		return pluginapi.Allow(), nil
	}
	if _, ok := p.roles[pluginapi.RoleAssistant]; !ok {
		return pluginapi.Allow(), nil
	}
	var targets []pluginapi.TextTarget
	for _, t := range x.Response.TextTargets() {
		if p.shouldRewrite(t.Text) {
			targets = append(targets, t)
		}
	}
	return pluginapi.Allow(), p.rewriteTargets(ctx, targets, x.Response.SetTargetText)
}

func (p *Plugin) shouldRewrite(text string) bool {
	if text == "" {
		return false
	}
	if p.cfg.SkipContentPrefix != "" && strings.HasPrefix(strings.TrimSpace(text), p.cfg.SkipContentPrefix) {
		return false
	}
	return true
}

// rewriteTargets rewrites every target concurrently (at most 8 in flight)
// and writes back the results that changed with set. A rewrite that fails
// fails the hook, so the instance's fail_mode decides whether the request
// is rejected or continues with the original text; nothing is written back
// in that case.
func (p *Plugin) rewriteTargets(ctx context.Context, targets []pluginapi.TextTarget, set func(pluginapi.TextTarget, string) error) error {
	if len(targets) == 0 {
		return nil
	}
	if p.host == nil {
		return errors.New("llm_based_altering: host is not initialized")
	}
	results := make([]string, len(targets))
	errs := make([]error, len(targets))
	sem := make(chan struct{}, maxConcurrentRewrites)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			rewritten, err := p.rewriteText(ctx, text)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = rewritten
		}(i, t.Text)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	for i, t := range targets {
		if results[i] == t.Text {
			continue
		}
		if err := set(t, results[i]); err != nil {
			return err
		}
	}
	return nil
}
