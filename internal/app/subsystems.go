package app

import (
	"errors"
	"reflect"
	"slices"
)

// Subsystem teardown.
//
// The gateway has two teardown paths, and they deliberately run different
// orders:
//
//   - Startup failure unwinds construction in strict reverse. Nothing ever
//     served traffic, so reverse construction is always safe.
//   - Runtime shutdown quiesces before it flushes, which is not the reverse of
//     construction: providers close first so the model refresh loop stops,
//     while the shared storage connection closes last, after every producer
//     has flushed into it.
//
// Neither order derives from the other, so the runtime order stays spelled out
// by hand in shutdownOrder. What this file removes is the drift risk of
// maintaining two independent lists: every subsystem registers exactly once
// during construction, and TestShutdownOrderCoversEveryRegisteredSubsystem
// fails when the hand-written order stops covering the registry.

// closerOwner names the teardown path that releases a registered subsystem at
// runtime. Startup failure releases every registered subsystem regardless of
// owner.
type closerOwner int

const (
	// ownedByShutdown is released by the ordered list in shutdownOrder.
	ownedByShutdown closerOwner = iota
	// ownedByPrologue is released before the HTTP server drains, because it
	// holds long-lived streams that would otherwise keep the drain open until
	// its timeout.
	ownedByPrologue
	// ownedByServer is released by Server.Shutdown once no request is in
	// flight, so in-flight work still reaches storage.
	ownedByServer
)

// Canonical subsystem names. Registration and shutdown order both reference
// these constants, so naming a subsystem in one place and not the other is a
// compile error rather than a silently uncovered subsystem. The values double
// as the identifiers in shutdown log and error messages.
const (
	subsystemLive                = "live broker"
	subsystemStorage             = "storage"
	subsystemRuntimeSettings     = "runtime settings"
	subsystemProviders           = "providers"
	subsystemAudit               = "audit"
	subsystemUsage               = "usage"
	subsystemBudgets             = "budgets"
	subsystemRateLimits          = "rate limits"
	subsystemBatch               = "batch store"
	subsystemFileStore           = "file store"
	subsystemResponseStore       = "response store"
	subsystemConversationStore   = "conversation store"
	subsystemProviderCredentials = "provider credentials"
	subsystemVirtualModels       = "virtual models"
	subsystemTagging             = "tagging"
	subsystemPricingOverrides    = "model pricing overrides"
	subsystemGuardrails          = "guardrails"
	subsystemWorkflows           = "workflows"
	subsystemAuthKeys            = "auth keys"
	subsystemUsers               = "users"
	subsystemMCPGateway          = "mcp gateway"
	subsystemResponseCache       = "response cache"
)

// registeredSubsystem is one initialized component together with the teardown
// path that releases it at runtime.
type registeredSubsystem struct {
	name  string
	owner closerOwner
	close func() error
}

// register records a successfully initialized subsystem. Registration order is
// construction order, which is what the startup-failure unwind reverses.
func (a *App) register(name string, owner closerOwner, closeFn func() error) {
	a.registered = append(a.registered, registeredSubsystem{name: name, owner: owner, close: closeFn})
}

// unwind closes every registered subsystem in reverse construction order and
// joins their errors. This is the startup-failure path: no request reached any
// of these components, so reverse construction is the correct order.
func (a *App) unwind() error {
	var errs []error
	for _, v := range slices.Backward(a.registered) {
		if closeFn := v.close; closeFn != nil {
			errs = append(errs, closeFn())
		}
	}
	return errors.Join(errs...)
}

// shutdownOrder is the runtime teardown order for subsystems owned by
// App.Shutdown: producers before the stores they write to, and the shared
// storage connection last of all. Order is load-bearing, so it is spelled out
// here rather than derived from construction order.
//
// Every ownedByShutdown registration must appear exactly once; the completeness
// test enforces that so a new subsystem cannot be released on startup failure
// while leaking on SIGTERM.
func (a *App) shutdownOrder() []registeredSubsystem {
	return []registeredSubsystem{
		// Stop live setting reconciliation before tearing down anything it can
		// reconfigure.
		{name: subsystemRuntimeSettings, close: closerOf(a.runtimeSettings)},
		// Stops model refresh and provider-owned resources.
		{name: subsystemProviders, close: closerOf(a.providers)},
		// Terminates upstream MCP sessions.
		{name: subsystemMCPGateway, close: closerOf(a.mcpGateway)},
		{name: subsystemProviderCredentials, close: closerOf(a.providerCredentials)},
		{name: subsystemVirtualModels, close: closerOf(a.virtualModels)},
		{name: subsystemTagging, close: closerOf(a.tagging)},
		{name: subsystemWorkflows, close: closerOf(a.workflows)},
		{name: subsystemPricingOverrides, close: closerOf(a.pricingOverrides)},
		{name: subsystemGuardrails, close: closerOf(a.guardrails)},
		{name: subsystemAuthKeys, close: closerOf(a.authKeys)},
		{name: subsystemUsers, close: closerOf(a.users)},
		{name: subsystemFileStore, close: closerOf(a.fileStore)},
		// The remaining stores flush buffered work into storage, so they must
		// close before the connection they write through.
		{name: subsystemBatch, close: closerOf(a.batch)},
		{name: subsystemBudgets, close: closerOf(a.budgets)},
		{name: subsystemRateLimits, close: closerOf(a.rateLimits)},
		{name: subsystemUsage, close: closerOf(a.usage)},
		{name: subsystemAudit, close: closerOf(a.audit)},
		{name: subsystemStorage, close: closerOf(a.storage)},
	}
}

// closerOf returns c.Close, or nil when there is nothing to close.
//
// Two distinct kinds of nil arrive here. A subsystem that never initialized is
// a typed-nil *Result, where taking the method value yields a non-nil func
// that panics on call. An unset storage.Storage is a nil interface, which
// reflect reports as an invalid value rather than a nil pointer.
func closerOf[T interface{ Close() error }](c T) func() error {
	switch value := reflect.ValueOf(c); value.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
	}
	return c.Close
}
