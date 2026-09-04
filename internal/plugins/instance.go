package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

// FailMode says what happens when an instance errors, panics, or times out.
type FailMode string

const (
	// FailClosed rejects the request with HTTP 500 and code "plugin_failure".
	FailClosed FailMode = "closed"
	// FailOpen logs the failure and continues as if the instance allowed.
	FailOpen FailMode = "open"
)

// ParseFailMode normalizes a configured fail mode. Empty selects the phase
// default.
func ParseFailMode(raw string) (FailMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case "closed", "close", "fail_closed", "fail-closed":
		return FailClosed, nil
	case "open", "fail_open", "fail-open":
		return FailOpen, nil
	default:
		return "", fmt.Errorf("invalid fail_mode %q (must be closed or open)", raw)
	}
}

// DefaultFailMode is the fail mode used when the instance does not set one:
// closed for content phases, open for everything else.
func DefaultFailMode(phase pluginapi.Kind) FailMode {
	switch phase {
	case pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream:
		return FailClosed
	default:
		return FailOpen
	}
}

// InstanceSpec is the operator-facing configuration of one instance.
type InstanceSpec struct {
	Name     string
	Config   json.RawMessage
	FailMode FailMode
	// Timeout bounds every hook call; zero means no per-instance timeout.
	Timeout time.Duration
}

// Instance is one configured, initialized plugin.
type Instance struct {
	Name     string
	Type     string
	Manifest pluginapi.Manifest
	Kinds    []pluginapi.Kind
	Plugin   pluginapi.Plugin
	FailMode FailMode
	Timeout  time.Duration
	// ConfigHash digests the validated config for chain hashing.
	ConfigHash string
}

// initTimeout bounds Init; a variable so tests can shorten it.
var initTimeout = 10 * time.Second

// NewInstance validates spec.Config against the entry's schema, builds a
// fresh plugin value and initializes it. Init runs with a 10s timeout and
// panic recovery.
func NewInstance(ctx context.Context, entry Entry, spec InstanceSpec, host pluginapi.Host) (inst *Instance, err error) {
	if entry.Factory == nil {
		return nil, fmt.Errorf("plugins: plugin %q has no factory", entry.Name)
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return nil, fmt.Errorf("plugins: instance name is required")
	}
	config, err := ValidateConfig(entry.Manifest.ConfigSchema, spec.Config, pluginapi.ScopeInstance)
	if err != nil {
		return nil, fmt.Errorf("plugins: instance %q of %q: %w", name, entry.Name, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if r := recover(); r != nil {
			inst = nil
			err = fmt.Errorf("plugins: instance %q of %q panicked during init: %v", name, entry.Name, r)
		}
	}()
	plugin := entry.Factory()
	if plugin == nil {
		return nil, fmt.Errorf("plugins: factory of %q returned nil", entry.Name)
	}
	if err := initPlugin(ctx, plugin, config, host); err != nil {
		return nil, fmt.Errorf("plugins: instance %q of %q: init: %w", name, entry.Name, err)
	}
	inst = &Instance{
		Name:       name,
		Type:       entry.Name,
		Manifest:   entry.Manifest,
		Kinds:      append([]pluginapi.Kind(nil), entry.Kinds...),
		Plugin:     plugin,
		FailMode:   spec.FailMode,
		Timeout:    spec.Timeout,
		ConfigHash: ConfigHash(config),
	}
	if inst.HasKind(pluginapi.KindStream) && inst.StreamPolicy().Mode == pluginapi.StreamBuffer && !inst.HasKind(pluginapi.KindResponse) {
		_ = inst.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("plugins: instance %q of %q: a buffer stream policy needs OnResponse, which %q does not implement", name, entry.Name, entry.Name)
	}
	return inst, nil
}

// initPlugin runs Init under the fixed init deadline. Init is expected to
// honour its context; one that does not is abandoned once the deadline
// passes and the instance is reported as failed.
func initPlugin(ctx context.Context, plugin pluginapi.Plugin, config json.RawMessage, host pluginapi.Host) error {
	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panicked during init: %v", r)
			}
		}()
		done <- plugin.Init(initCtx, config, host)
	}()
	select {
	case err := <-done:
		if err == nil && initCtx.Err() != nil {
			return fmt.Errorf("%w after the %s init deadline", ErrAbandoned, initTimeout)
		}
		return err
	case <-initCtx.Done():
		if errors.Is(initCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: exceeded the %s init deadline", ErrAbandoned, initTimeout)
		}
		return fmt.Errorf("%w: %v", ErrAbandoned, initCtx.Err())
	}
}

// FailsOpen reports whether err from a hook of phase is absorbed under the
// instance's fail mode. An abandoned call (ErrAbandoned) never fails open
// when shared says the hook ran on the request's own exchange: it may still
// be editing it, so the request cannot safely continue.
func (i *Instance) FailsOpen(phase pluginapi.Kind, err error, shared bool) bool {
	if i.EffectiveFailMode(phase) != FailOpen {
		return false
	}
	return !shared || !errors.Is(err, ErrAbandoned)
}

// Close releases the plugin's resources, recovering panics.
func (i *Instance) Close(ctx context.Context) (err error) {
	if i == nil || i.Plugin == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugins: instance %q panicked during close: %v", i.Name, r)
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	return i.Plugin.Close(ctx)
}

// HasKind reports whether the instance implements the hook.
func (i *Instance) HasKind(kind pluginapi.Kind) bool {
	return i != nil && hasKind(i.Kinds, kind)
}

// Mutates reports whether the plugin declares that it edits content.
func (i *Instance) Mutates() bool {
	return i != nil && i.Manifest.Mutates
}

// EffectiveFailMode resolves the fail mode for a phase.
func (i *Instance) EffectiveFailMode(phase pluginapi.Kind) FailMode {
	if i != nil && i.FailMode != "" {
		return i.FailMode
	}
	return DefaultFailMode(phase)
}

// StreamPolicy returns the stream policy of a stream-hook instance, or the
// zero policy (observe) for others. A panicking StreamPolicy is logged and
// treated as observe.
func (i *Instance) StreamPolicy() (policy pluginapi.StreamPolicy) {
	if i == nil {
		return pluginapi.StreamPolicy{}
	}
	hook, ok := i.Plugin.(pluginapi.StreamHook)
	if !ok {
		return pluginapi.StreamPolicy{}
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("plugin panicked in StreamPolicy; treating as observe", "plugin", i.Type, "instance", i.Name, "panic", r)
			policy = pluginapi.StreamPolicy{Mode: pluginapi.StreamObserve}
		}
	}()
	policy = hook.StreamPolicy()
	if policy.Mode == "" {
		policy.Mode = pluginapi.StreamObserve
	}
	return policy
}
