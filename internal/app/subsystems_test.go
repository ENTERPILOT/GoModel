package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/providers"
)

type lifecycleRuntimeSetting struct{}

func (*lifecycleRuntimeSetting) Descriptor() ext.SettingDescriptor {
	return ext.SettingDescriptor{Key: "test.runtime", Value: "fixed", Locked: true}
}

func (*lifecycleRuntimeSetting) Apply(string) error { return nil }

// newFullyWiredApp builds an App with every optional subsystem enabled, so the
// coverage tests below see the complete registry rather than the subset a
// minimal config happens to initialize.
func newFullyWiredApp(t *testing.T) *App {
	t.Helper()

	// Load() reads the ambient environment, so pin storage and keep startup
	// local: no model discovery, no outbound calls.
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "subsystems.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	// The subsystems guarded by their own flags must be present, or a missing
	// entry in shutdownOrder would go unnoticed here.
	t.Setenv("MCP_ENABLED", "true")
	t.Setenv("USAGE_ENABLED", "true")
	t.Setenv("BUDGETS_ENABLED", "true")
	t.Setenv("RATE_LIMITS_ENABLED", "true")

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	extensions := &ext.Registry{}
	extensions.RegisterSetting(&lifecycleRuntimeSetting{})
	application, err := New(context.Background(), Config{
		AppConfig:  loaded,
		Factory:    providers.NewProviderFactory(),
		Extensions: extensions,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := application.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return application
}

// The startup-failure unwind and the runtime shutdown order are maintained
// separately and deliberately differ (reverse construction versus
// quiesce-then-flush). Nothing but this test stops them from diverging in
// content: a subsystem registered but absent from shutdownOrder is released on
// a failed startup and leaked on every SIGTERM, which no other test would
// catch.
func TestShutdownOrderCoversEveryRegisteredSubsystem(t *testing.T) {
	application := newFullyWiredApp(t)

	ordered := make([]string, 0, len(application.shutdownOrder()))
	for _, subsystem := range application.shutdownOrder() {
		ordered = append(ordered, subsystem.name)
	}

	for _, registered := range application.registered {
		if registered.owner != ownedByShutdown {
			continue
		}
		if !slices.Contains(ordered, registered.name) {
			t.Errorf("subsystem %q is registered as ownedByShutdown but missing from shutdownOrder: "+
				"it would be released on startup failure and leaked on shutdown", registered.name)
		}
	}
}

func TestShutdownStopsRuntimeSettingsBeforeProviders(t *testing.T) {
	order := newFullyWiredApp(t).shutdownOrder()
	runtimeSettingsIndex := slices.IndexFunc(order, func(subsystem registeredSubsystem) bool {
		return subsystem.name == subsystemRuntimeSettings
	})
	providersIndex := slices.IndexFunc(order, func(subsystem registeredSubsystem) bool {
		return subsystem.name == subsystemProviders
	})
	if runtimeSettingsIndex < 0 || providersIndex < 0 || runtimeSettingsIndex >= providersIndex {
		t.Fatalf("shutdown order must stop runtime settings before providers")
	}
}

// The mirror of the check above: an entry in shutdownOrder that nothing
// registers is either a stale leftover or a subsystem whose owner was
// mislabelled, and it silently closes nothing.
func TestShutdownOrderHasNoUnregisteredEntries(t *testing.T) {
	application := newFullyWiredApp(t)

	registered := make(map[string]closerOwner, len(application.registered))
	for _, subsystem := range application.registered {
		// Keying by name collapses duplicates, which would let two registrations
		// share one shutdown-order entry and pass the coverage check below.
		if _, duplicate := registered[subsystem.name]; duplicate {
			t.Errorf("subsystem %q is registered more than once, so the coverage check below cannot see the duplicate", subsystem.name)
		}
		registered[subsystem.name] = subsystem.owner
	}

	seen := make(map[string]bool, len(application.shutdownOrder()))
	for _, subsystem := range application.shutdownOrder() {
		if seen[subsystem.name] {
			t.Errorf("subsystem %q appears twice in shutdownOrder", subsystem.name)
		}
		seen[subsystem.name] = true

		owner, ok := registered[subsystem.name]
		if !ok {
			t.Errorf("shutdownOrder closes unregistered subsystem %q", subsystem.name)
			continue
		}
		if owner != ownedByShutdown {
			t.Errorf("subsystem %q is closed by shutdownOrder but registered as owner %d", subsystem.name, owner)
		}
	}
}

// The registry promises each subsystem registers exactly once. Nothing but this
// test enforces it: a second registration under an existing name is invisible to
// the name-keyed coverage checks, while unwind walks the append-only slice and
// would close that resource twice on startup failure.
func TestEverySubsystemRegistersExactlyOnce(t *testing.T) {
	application := newFullyWiredApp(t)

	counts := make(map[string]int, len(application.registered))
	for _, subsystem := range application.registered {
		counts[subsystem.name]++
	}
	for name, count := range counts {
		if count > 1 {
			t.Errorf("subsystem %q is registered %d times; unwind would close it %d times on startup failure", name, count, count)
		}
	}
}

// Subsystems owned by another teardown path must stay out of shutdownOrder:
// the response cache and the response/conversation stores are released by
// Server.Shutdown after in-flight requests drain, and closing them earlier
// would drop writes that are still arriving.
func TestNonShutdownOwnedSubsystemsAreRegisteredButNotInShutdownOrder(t *testing.T) {
	application := newFullyWiredApp(t)

	owners := make(map[string]closerOwner, len(application.registered))
	for _, subsystem := range application.registered {
		owners[subsystem.name] = subsystem.owner
	}

	for name, wantOwner := range map[string]closerOwner{
		subsystemResponseStore:     ownedByServer,
		subsystemConversationStore: ownedByServer,
		subsystemResponseCache:     ownedByServer,
		subsystemLive:              ownedByPrologue,
	} {
		owner, ok := owners[name]
		if !ok {
			t.Errorf("subsystem %q is never registered", name)
			continue
		}
		if owner != wantOwner {
			t.Errorf("subsystem %q registered with owner %d, want %d", name, owner, wantOwner)
		}
	}
}

// Startup failure must close what was already built, newest first, so a
// half-built app never leaks the resources it did acquire.
func TestUnwindClosesInReverseRegistrationOrder(t *testing.T) {
	var closed []string
	application := &App{}
	for _, name := range []string{"first", "second", "third"} {
		application.register(name, ownedByShutdown, func() error {
			closed = append(closed, name)
			return nil
		})
	}

	if err := application.unwind(); err != nil {
		t.Fatalf("unwind: %v", err)
	}
	if want := []string{"third", "second", "first"}; !slices.Equal(closed, want) {
		t.Errorf("closed %v, want %v", closed, want)
	}
}

// One failing closer must not strand the rest: unwind runs every registered
// closer and reports the failures together.
func TestUnwindClosesEveryEntryAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first boom")
	secondErr := errors.New("second boom")

	closed := 0
	application := &App{}
	application.register("healthy", ownedByShutdown, func() error { closed++; return nil })
	application.register("broken", ownedByShutdown, func() error { closed++; return firstErr })
	application.register("also-broken", ownedByShutdown, func() error { closed++; return secondErr })
	// A nil closer is skipped rather than panicking.
	application.register("nothing-to-close", ownedByShutdown, nil)

	err := application.unwind()
	if closed != 3 {
		t.Errorf("closed %d subsystems, want 3", closed)
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Errorf("unwind error %v does not carry both close failures", err)
	}
}
