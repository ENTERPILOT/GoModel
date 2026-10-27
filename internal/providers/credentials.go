package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/encryption"
)

// ErrCredentialNotFound indicates a requested admin-managed provider
// credential row was not found.
var ErrCredentialNotFound = errors.New("provider credential not found")

// ManagedProviderCredential is one admin-managed provider instance: the
// dashboard equivalent of a `providers.<name>:` entry in config.yaml. Name is
// the configured provider instance name (what shows up as the qualifier in
// "name/model" selectors); Type selects the registered adapter (openai,
// anthropic, ollama, vertex, ...).
type ManagedProviderCredential struct {
	Name string
	Type string

	// APIKeys is the ordered credential rotation set. Keyless providers
	// (Ollama, vLLM) and Vertex/Bedrock (which authenticate a different way)
	// may leave this empty.
	APIKeys []string
	// SessionStickyKeys is nil for legacy/default rows and therefore means
	// enabled. A non-nil false value is the explicit dashboard/YAML opt-out.
	SessionStickyKeys *bool

	BaseURL                  string
	APIVersion               string
	Backend                  string
	AuthType                 string
	APIMode                  string
	VertexProject            string
	VertexLocation           string
	ServiceAccountFile       string
	ServiceAccountJSON       string
	ServiceAccountJSONBase64 string
	GCPScope                 string
	// ProxyURL is this provider's outbound HTTP(S)/SOCKS5 proxy; empty means
	// the gateway-wide default. It may carry credentials, so the admin API
	// masks it on the way out.
	ProxyURL string
	Models   []string

	// Enabled controls whether this credential is applied to the running
	// registry. Disabling one keeps the row (and its keys) on file without
	// routing traffic to it — the same effect as deleting it, without losing
	// the configuration.
	Enabled bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// toRawProviderConfig converts the admin row into the same shape config.yaml
// providers resolve from, so it can run through the existing credential
// pipeline (filterProviders, buildProviderConfig) unmodified.
func (m ManagedProviderCredential) toRawProviderConfig() config.RawProviderConfig {
	raw := config.RawProviderConfig{
		Type:                     m.Type,
		APIKeys:                  m.APIKeys,
		SessionStickyKeys:        m.SessionStickyKeys,
		BaseURL:                  m.BaseURL,
		APIVersion:               m.APIVersion,
		Backend:                  m.Backend,
		AuthType:                 m.AuthType,
		APIMode:                  m.APIMode,
		VertexProject:            m.VertexProject,
		VertexLocation:           m.VertexLocation,
		ServiceAccountFile:       m.ServiceAccountFile,
		ServiceAccountJSON:       m.ServiceAccountJSON,
		ServiceAccountJSONBase64: m.ServiceAccountJSONBase64,
		GCPScope:                 m.GCPScope,
		ProxyURL:                 m.ProxyURL,
		Models:                   rawProviderModelsFromIDs(m.Models),
	}
	if len(m.APIKeys) > 0 {
		raw.APIKey = m.APIKeys[0]
	}
	return raw
}

// CredentialStore persists admin-managed provider credentials.
type CredentialStore interface {
	List(ctx context.Context) ([]ManagedProviderCredential, error)
	Get(ctx context.Context, name string) (*ManagedProviderCredential, error)
	Upsert(ctx context.Context, cred ManagedProviderCredential) error
	Delete(ctx context.Context, name string) error
	Close() error
}

// CredentialsService merges declarative (config.yaml / env var) provider
// names with the admin-managed credential store and reconciles the result
// into the live factory/registry — the provider-credentials equivalent of
// mcpgateway.Service. Declarative names are read-only here (shadowed),
// mirroring every other config-store precedence rule in the gateway.
type CredentialsService struct {
	factory  *ProviderFactory
	registry *ModelRegistry
	store    CredentialStore

	managedNames map[string]struct{}
	resilience   config.ResilienceConfig
	// skipped holds, by name, why Reload left out stored credentials that
	// did not build. Guarded by mu.
	skipped map[string]error
	// secrets resolves the secret references stored credentials hold, with
	// the schemes of the generation this service belongs to.
	secrets *config.Secrets

	// applyMu serializes how saves, deletes, and secret rotation commit.
	// Saves and rotation resolve and build without it, then, holding it,
	// check that nothing was installed under that name meanwhile.
	applyMu sync.Mutex

	// configs holds the effective ProviderConfig of every credential that is
	// currently installed in the registry, so the admin status endpoint can
	// report the same merged resilience settings for dashboard-registered
	// providers that it reports for config.yaml/env ones.
	mu      sync.RWMutex
	configs map[string]ProviderConfig
	// keyrings holds each installed provider's keyring, so a rotated API key
	// is swapped in place rather than rebuilding the provider.
	keyrings map[string]*Keyring
	// rows holds the stored row each installed provider was built from, so a
	// refresh rebuilds only the providers whose row changed.
	rows map[string]ManagedProviderCredential
	// revisions counts, by name, every install, removal, and in-place key
	// swap, so work resolved before one of them can tell. Never reset, so a
	// count cannot repeat.
	revisions map[string]uint64

	// changed is called after every successful save or delete; see
	// SetChangeNotifier.
	changed func(context.Context)
}

// SetChangeNotifier installs fn, called after every successful save or
// delete so other gateway instances reconcile their registries. Call it
// before serving.
func (s *CredentialsService) SetChangeNotifier(fn func(context.Context)) {
	if s != nil {
		s.changed = fn
	}
}

func (s *CredentialsService) notifyChanged(ctx context.Context) {
	if s.changed != nil {
		s.changed(ctx)
	}
}

// NewCredentialsService builds the service and applies every currently
// stored, enabled, non-shadowed credential to the registry before returning.
// secrets resolves the secret references credentials hold; nil resolves the
// built-in env and file schemes only.
func NewCredentialsService(ctx context.Context, factory *ProviderFactory, registry *ModelRegistry, store CredentialStore, declaredNames []string, resilience config.ResilienceConfig, secrets *config.Secrets) (*CredentialsService, error) {
	if factory == nil {
		return nil, fmt.Errorf("provider factory is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("model registry is required")
	}
	if store == nil {
		return nil, fmt.Errorf("credential store is required")
	}

	managed := make(map[string]struct{}, len(declaredNames))
	for _, name := range declaredNames {
		name = strings.TrimSpace(name)
		if name != "" {
			managed[name] = struct{}{}
		}
	}

	s := &CredentialsService{
		factory:      factory,
		registry:     registry,
		store:        store,
		managedNames: managed,
		resilience:   resilience,
		secrets:      secrets,
		configs:      make(map[string]ProviderConfig),
		keyrings:     make(map[string]*Keyring),
		rows:         make(map[string]ManagedProviderCredential),
		revisions:    make(map[string]uint64),
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// RegisteredTypes lists every provider type the factory can construct, for
// the admin create-form's type picker.
func (s *CredentialsService) RegisteredTypes() []string {
	return s.factory.RegisteredTypes()
}

// CredentialSchemas returns the credential form of every registered provider
// type, so the admin create/edit form can offer exactly the fields the
// selected type uses.
func (s *CredentialsService) CredentialSchemas() []CredentialSchema {
	return s.factory.CredentialSchemas()
}

// CredentialSchema returns one provider type's credential form.
func (s *CredentialsService) CredentialSchema(providerType string) CredentialSchema {
	return credentialSchema(providerType, s.factory.discoveryConfig(providerType))
}

// IsManaged reports whether name is declared in config.yaml/env, making it
// read-only from the admin API's point of view.
func (s *CredentialsService) IsManaged(name string) bool {
	_, ok := s.managedNames[strings.TrimSpace(name)]
	return ok
}

// Reload registers every enabled, non-shadowed stored credential into the
// registry (pure in-memory work) and then kicks a non-blocking model-inventory
// fetch so the newly registered providers' models become routable without
// delaying the caller — the same async startup contract providers.Init uses
// for declarative providers, so adding the credentials store never turns
// gateway startup into a synchronous network round trip to every provider.
// Declarative providers are untouched: providers.Init already registered them.
func (s *CredentialsService) Reload(ctx context.Context) error {
	installed, err := s.reconcile(ctx)
	if installed {
		s.registry.InitializeAsync(ctx)
	}
	return err
}

// Refresh brings the registry in line with the stored rows, which another
// gateway instance sharing the database may have changed: it installs new
// and edited credentials, and unregisters deleted and disabled ones.
// Unchanged credentials keep their running provider. The model inventory of
// newly installed providers is fetched in the background, bounded by ctx,
// even when a later pass fails: the next Refresh sees them as running and
// would not fetch it again.
func (s *CredentialsService) Refresh(ctx context.Context) error {
	installed, err := s.reconcile(ctx)
	if installed {
		go func() {
			refreshCtx, cancel := context.WithTimeout(ctx, credentialModelRefreshTimeout)
			defer cancel()
			if err := s.registry.Refresh(refreshCtx); err != nil && ctx.Err() == nil {
				slog.Warn("provider credentials reconciled but the model catalog refresh failed", "error", err)
			}
		}()
	}
	return err
}

// credentialModelRefreshTimeout bounds the background model fetch after a
// Refresh, as InitializeAsync bounds the one at startup.
const credentialModelRefreshTimeout = 60 * time.Second

// reconcile applies the stored rows to the registry, reporting whether it
// installed a provider. A credential that no longer builds keeps whatever is
// running under its name and is reported by Skipped.
//
// Like a save, it builds without holding applyMu and installs or removes only
// while the credential's revision is still the one it saw before reading the
// rows. A save, delete, or rotation on this instance in the meantime may have
// applied an older row than the one read, so the rows are read and applied
// again until a pass skips nothing.
func (s *CredentialsService) reconcile(ctx context.Context) (bool, error) {
	installedAny := false
	for {
		installed, stale, err := s.reconcileOnce(ctx)
		installedAny = installedAny || installed
		if err != nil || !stale {
			return installedAny, err
		}
	}
}

// reconcileOnce is one pass of reconcile. It reports stale when it skipped a
// credential that changed on this instance while it read the rows.
func (s *CredentialsService) reconcileOnce(ctx context.Context) (installedAny, stale bool, err error) {
	before := s.revisionsSnapshot()
	rows, err := s.store.List(ctx)
	if err != nil {
		return false, false, fmt.Errorf("list provider credentials: %w", err)
	}

	skipped := make(map[string]error)
	stored := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		row.Name = strings.TrimSpace(row.Name)
		if s.IsManaged(row.Name) {
			slog.Warn("provider credential from admin store is shadowed by config/env", "provider", row.Name)
			continue
		}
		stored[row.Name] = struct{}{}
		if !row.Enabled {
			stale = !s.removeUnlessChanged(row.Name, before[row.Name]) || stale
			continue
		}
		if s.runsRow(row) {
			continue
		}
		built, err := s.buildStored(ctx, row)
		if err != nil {
			slog.Error("failed to apply stored provider credential", "provider", row.Name, "error", err)
			skipped[row.Name] = err
			continue
		}
		if s.installUnlessChanged(row.Name, before[row.Name], built) {
			installedAny = true
		} else {
			stale = true
		}
	}
	for _, name := range s.installedNames() {
		if _, ok := stored[name]; !ok {
			stale = !s.removeUnlessChanged(name, before[name]) || stale
		}
	}

	s.mu.Lock()
	s.skipped = skipped
	s.mu.Unlock()
	return installedAny, stale, nil
}

// runsRow reports whether the provider installed under row's name was built
// from this very row.
func (s *CredentialsService) runsRow(row ManagedProviderCredential) bool {
	s.mu.RLock()
	installed, ok := s.rows[row.Name]
	s.mu.RUnlock()
	return ok && sameStoredCredential(&installed, &row)
}

// installUnlessChanged installs built under name unless name was installed,
// removed, or rotated since revision was read, reporting whether it did.
func (s *CredentialsService) installUnlessChanged(name string, revision uint64, built builtCredential) bool {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.revision(name) != revision {
		return false
	}
	s.install(name, built)
	return true
}

// removeUnlessChanged unregisters name, when it is installed, unless it was
// installed, removed, or rotated since revision was read; it reports false
// only in that last case. The secrets the stored row held are not released
// here: the instance that deleted or disabled it already did.
func (s *CredentialsService) removeUnlessChanged(name string, revision uint64) bool {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.revision(name) != revision {
		return false
	}
	if s.Installed(name) {
		s.remove(name)
		slog.Info("unregistered provider credential removed or disabled on another instance", "provider", name)
	}
	return true
}

// revisionsSnapshot copies the revision of every credential.
func (s *CredentialsService) revisionsSnapshot() map[string]uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.revisions)
}

// installedNames lists the credentials installed in the registry.
func (s *CredentialsService) installedNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Collect(maps.Keys(s.configs))
}

// Skipped returns, by name, why the last Reload left out stored credentials
// that did not build: a secret reference that did not resolve, a resolved
// value that is not usable, such as an empty key, or any other reason.
func (s *CredentialsService) Skipped() map[string]error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.skipped)
}

// Installed reports whether the credential name is installed in the registry.
func (s *CredentialsService) Installed(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.configs[name]
	return ok
}

// List returns every admin-managed credential row (secrets included; callers
// at the HTTP boundary are responsible for redaction).
func (s *CredentialsService) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	return s.store.List(ctx)
}

// Get returns one admin-managed credential row.
func (s *CredentialsService) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	return s.store.Get(ctx, name)
}

// Upsert validates, persists, and hot-applies one admin-managed provider
// credential. A disabled row is persisted but unregistered from the live
// registry, the same end state as deleting it.
func (s *CredentialsService) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	name := strings.TrimSpace(cred.Name)
	if name == "" {
		return &CredentialFieldError{Field: "name", Message: "provider name is required"}
	}
	if s.IsManaged(name) {
		return fmt.Errorf("provider %q is managed by config/env and is read-only", name)
	}
	if strings.TrimSpace(cred.Type) == "" {
		return &CredentialFieldError{Field: "type", Message: "provider type is required"}
	}
	// "/" is the model-selector qualifier delimiter ("name/model"); a name
	// containing one would make the provider unreachable or ambiguous
	// through that syntax.
	if strings.Contains(name, "/") {
		return &CredentialFieldError{Field: "name", Message: fmt.Sprintf("provider name %q must not contain '/'", name)}
	}
	if !s.factory.knowsType(cred.Type) {
		return &CredentialFieldError{Field: "type", Message: "unknown provider type: " + cred.Type}
	}
	cred.Name = name
	// ErrSealUnconfirmed means the row was written and applied: refresh like
	// any other save, then report the error.
	saveErr := s.apply(ctx, cred)
	if saveErr != nil && !errors.Is(saveErr, encryption.ErrSealUnconfirmed) {
		return saveErr
	}
	s.notifyChanged(ctx)
	// A Refresh error here means the provider's /models call itself failed
	// (bad key, unreachable host, ...) -- registration still succeeded, and
	// the provider correctly shows Unhealthy on the status endpoint instead
	// of silently vanishing. That is not a save failure: an admin fixing a
	// typo'd API key should see "saved", not a scary error on every attempt
	// until the key happens to be valid.
	if err := s.registry.Refresh(ctx); err != nil {
		slog.Warn("provider credential saved but its model catalog refresh failed", "provider", name, "error", err)
	}
	return saveErr
}

// Delete removes one admin-managed provider credential and unregisters it
// from the live registry.
func (s *CredentialsService) Delete(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if s.IsManaged(name) {
		return fmt.Errorf("provider %q is managed by config/env and is read-only", name)
	}
	if err := s.deleteStored(ctx, name); err != nil {
		return err
	}
	s.notifyChanged(ctx)
	// See the matching comment in Upsert: a Refresh failure here reflects a
	// remaining provider's own health, not whether the delete succeeded.
	if err := s.registry.Refresh(ctx); err != nil {
		slog.Warn("provider credential deleted but the model catalog refresh failed", "provider", name, "error", err)
	}
	return nil
}

// deleteStored deletes the stored row name, unregisters it, and releases the
// secrets it held.
func (s *CredentialsService) deleteStored(ctx context.Context, name string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	previous, err := s.store.Get(ctx, name)
	if err != nil && !errors.Is(err, ErrCredentialNotFound) {
		return err
	}
	if err := s.store.Delete(ctx, name); err != nil {
		return err
	}
	s.remove(name)
	s.releaseSecrets(ctx, name, credentialSecretValues(previous), nil)
	return nil
}

// apply validates, persists, and installs one credential row: the part of
// Upsert that touches the store and the registry.
//
// Literal secrets are written through the generation's SecretWriter first,
// when one is registered, so the row is stored with references only. The row
// is then resolved and its adapter constructed before anything is persisted
// or the registry touched: an unresolvable row is not worth storing, and if
// this is an edit, whatever is currently registered under this name --
// possibly a working provider actively serving traffic -- must keep serving
// rather than being unregistered out from under a failed edit.
//
// Resolving waits on secret backends, so it runs before applyMu is taken: a
// save stuck on one reference must not hold up other saves, deletes, or
// rotation. Under the lock, the stored row must still be the one the save
// started from, or the secrets it keeps may already be released.
func (s *CredentialsService) apply(ctx context.Context, cred ManagedProviderCredential) error {
	previous, err := s.storedCredential(ctx, cred.Name)
	if err != nil {
		return err
	}
	if err := config.CheckSecretDestinations(ctx, CredentialSecretEntity, cred.Name, credentialDestinations(previous, &cred), credentialSecretValues(&cred)); err != nil {
		return err
	}
	kept := credentialSecretValues(previous)
	written, err := s.storeCredentialSecrets(ctx, &cred, kept)
	if err != nil {
		return err
	}
	var built builtCredential
	for {
		revision := s.revision(cred.Name)
		built, err = s.buildCredential(ctx, cred)
		if err != nil {
			// Nothing was stored, so whatever the stored row holds stays.
			s.releaseSecrets(ctx, cred.Name, written, kept)
			return err
		}
		s.applyMu.Lock()
		if s.revision(cred.Name) == revision {
			break
		}
		// Rotation or another save installed this credential while it was
		// resolving: resolve again rather than replace newer values.
		s.applyMu.Unlock()
	}
	defer s.applyMu.Unlock()
	current, err := s.storedCredential(ctx, cred.Name)
	if err == nil && !sameStoredCredential(previous, current) {
		err = &CredentialFieldError{Message: fmt.Sprintf("provider %q was changed while this save was in progress; reload it and save again", cred.Name)}
	}
	if err != nil {
		s.releaseSecrets(ctx, cred.Name, written, kept)
		return err
	}
	// ErrSealUnconfirmed means the row was written: apply it like any other
	// save, then report the error.
	saveErr := s.store.Upsert(ctx, cred)
	if saveErr != nil && !errors.Is(saveErr, encryption.ErrSealUnconfirmed) {
		// The stored row is unchanged, so whatever it holds stays.
		s.releaseSecrets(ctx, cred.Name, written, kept)
		return saveErr
	}

	if cred.Enabled {
		s.install(cred.Name, built)
	} else {
		s.remove(cred.Name)
	}
	s.releaseSecrets(ctx, cred.Name, kept, credentialSecretValues(&cred))
	return saveErr
}

// revision returns how often name was installed, removed, or had its keys
// swapped.
func (s *CredentialsService) revision(name string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revisions[name]
}

// storedCredential returns the stored row name, or nil when there is none.
func (s *CredentialsService) storedCredential(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	row, err := s.store.Get(ctx, name)
	if errors.Is(err, ErrCredentialNotFound) {
		return nil, nil
	}
	return row, err
}

// sameStoredCredential reports whether two reads of one stored row hold the
// same credential, ignoring timestamps.
func sameStoredCredential(a, b *ManagedProviderCredential) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := comparableCredential(*a), comparableCredential(*b)
	return reflect.DeepEqual(x, y)
}

// comparableCredential clears what a store round trip may change without
// changing the credential: the timestamps, an unset session stickiness read
// back as its enabled default, and an empty list read back as nil or empty.
func comparableCredential(c ManagedProviderCredential) ManagedProviderCredential {
	c.CreatedAt, c.UpdatedAt = time.Time{}, time.Time{}
	sticky := sessionStickyKeysEnabled(c.SessionStickyKeys)
	c.SessionStickyKeys = &sticky
	if len(c.APIKeys) == 0 {
		c.APIKeys = nil
	}
	if len(c.Models) == 0 {
		c.Models = nil
	}
	return c
}

// builtCredential is a credential row resolved and constructed, ready to
// install. provider is nil for a disabled row.
type builtCredential struct {
	provider core.Provider
	cfg      ProviderConfig
	keys     *Keyring
	secrets  *config.ResolvedEntity
	// row is the stored row the provider was built from.
	row ManagedProviderCredential
}

// buildCredential resolves the row's secret references, validates the result
// against the type's credential form, and constructs the adapter when the row
// is enabled. A disabled row is still resolved, so a reference that cannot be
// resolved is rejected when it is saved rather than when it is enabled.
func (s *CredentialsService) buildCredential(ctx context.Context, cred ManagedProviderCredential) (builtCredential, error) {
	resolved, secrets, err := s.resolveCredential(ctx, cred)
	if err != nil {
		return builtCredential{}, err
	}
	schema := s.CredentialSchema(cred.Type)
	if err := validateCredential(resolved, schema); err != nil {
		return builtCredential{}, err
	}
	if !cred.Enabled {
		return builtCredential{}, nil
	}
	built, err := s.buildProvider(cred, resolved)
	if err != nil {
		return builtCredential{}, unappliableCredentialError(cred, schema, err)
	}
	built.secrets = secrets
	return built, nil
}

// buildStored resolves one stored, enabled credential row and constructs its
// adapter without touching the registry. Unlike a save it does not validate
// the row against the type's credential form: the row was validated when it
// was saved, and a stricter form in a later release must not take a working
// provider offline.
func (s *CredentialsService) buildStored(ctx context.Context, row ManagedProviderCredential) (builtCredential, error) {
	resolved, secrets, err := s.resolveCredential(ctx, row)
	if err != nil {
		return builtCredential{}, err
	}
	built, err := s.buildProvider(row, resolved)
	if err != nil {
		return builtCredential{}, err
	}
	built.secrets = secrets
	return built, nil
}

// buildProvider runs one credential row, as stored and with its references
// resolved, through the same pipeline declarative providers use
// (env-placeholder rejection, API key de-duplication, resilience merge) and
// constructs the adapter, without touching the registry. Callers that already
// have something live registered under this name must build+validate first
// and only call install on success, so a bad edit never displaces a working
// provider.
func (s *CredentialsService) buildProvider(stored, resolved ManagedProviderCredential) (builtCredential, error) {
	cfg, err := s.providerConfig(stored, resolved)
	if err != nil {
		return builtCredential{}, err
	}
	keys := NewKeyringWithSessionStickiness(cfg.SessionStickyKeys, cfg.APIKeys...)
	provider, err := s.factory.create(cfg, keys)
	if err != nil {
		return builtCredential{}, err
	}
	return builtCredential{provider: provider, cfg: cfg, keys: keys, row: stored}, nil
}

// providerConfig turns one row into the effective configuration of its
// provider, filtering credentials the way providers.Init does for config.yaml:
// the stored values are checked before resolution, where a legacy ${VAR}
// placeholder does not count as set and an API key holding one is dropped,
// and the resolved values only have to be non-empty, because a resolved
// secret is data and may contain "${". resolved is stored with its references
// resolved by resolveCredential, key for key.
func (s *CredentialsService) providerConfig(stored, resolved ManagedProviderCredential) (ProviderConfig, error) {
	name := strings.TrimSpace(resolved.Name)
	discovery := s.factory.discoveryConfigsSnapshot()
	var storedKeys, resolvedKeys []string
	for i, key := range stored.APIKeys {
		if providerValueSet(key) && i < len(resolved.APIKeys) {
			storedKeys = append(storedKeys, key)
			resolvedKeys = append(resolvedKeys, resolved.APIKeys[i])
		}
	}

	before := stored.toRawProviderConfig()
	before.APIKey, before.APIKeys = firstAndAll(storedKeys)
	if _, ok := filterProviders(map[string]config.RawProviderConfig{name: before}, discovery, providerValueSet)[name]; !ok {
		return ProviderConfig{}, errCredentialsDidNotResolve
	}
	after := resolved.toRawProviderConfig()
	after.APIKey, after.APIKeys = firstAndAll(dedupeResolvedKeys(resolvedKeys))
	rawCfg, ok := filterProviders(map[string]config.RawProviderConfig{name: after}, discovery, resolvedValueSet)[name]
	if !ok {
		return ProviderConfig{}, errCredentialsDidNotResolve
	}
	cfg := buildProviderConfig(rawCfg, s.resilience)
	cfg.Name = name
	return cfg, nil
}

var errCredentialsDidNotResolve = errors.New("credentials did not resolve (missing API key or required fields)")

// dedupeResolvedKeys trims resolved API keys, drops empty ones, and collapses
// repeats into the first occurrence. A "${" in a resolved key is kept.
func dedupeResolvedKeys(keys []string) []string {
	sourced := make([]sourcedKey, len(keys))
	for i, key := range keys {
		sourced[i] = sourcedKey{Value: key}
	}
	_, values := keyValues(dedupeKeys(sourced, resolvedValueSet))
	return values
}

// firstAndAll returns keys as RawProviderConfig holds them: the first in
// APIKey, every one in APIKeys.
func firstAndAll(keys []string) (string, []string) {
	if len(keys) == 0 {
		return "", nil
	}
	return keys[0], keys
}

// install unregisters whatever is currently registered under name (a no-op
// if nothing is), registers the built provider in its place, records its
// effective configuration, and records its secret references for rotation.
func (s *CredentialsService) install(name string, built builtCredential) {
	cfg := built.cfg
	s.registry.UnregisterProvider(name)
	s.registry.RegisterProviderWithNameAndType(built.provider, name, cfg.Type)
	if len(cfg.Models) > 0 {
		s.registry.SetProviderConfiguredModels(name, cfg.Models)
	}
	if len(cfg.ModelMetadataOverrides) > 0 {
		s.registry.SetProviderMetadataOverrides(name, cfg.ModelMetadataOverrides)
	}
	s.registry.SetProviderModelFilter(name, cfg.ModelFilter)

	s.mu.Lock()
	s.configs[name] = cfg
	s.keyrings[name] = built.keys
	s.rows[name] = built.row
	s.revisions[name]++
	s.mu.Unlock()
	built.secrets.Record()
}

// remove unregisters name from the registry and forgets its effective
// configuration and secret references.
func (s *CredentialsService) remove(name string) {
	s.registry.UnregisterProvider(name)

	s.mu.Lock()
	delete(s.configs, name)
	delete(s.keyrings, name)
	delete(s.rows, name)
	s.revisions[name]++
	s.mu.Unlock()
	s.secrets.ForgetEntity(credentialSecretEntity(name))
}

// ConfiguredProviders returns the admin-safe effective configuration of every
// credential currently installed in the registry, in the same shape
// providers.Init produces for config.yaml/env providers.
func (s *CredentialsService) ConfiguredProviders() []SanitizedProviderConfig {
	s.mu.RLock()
	configs := make(map[string]ProviderConfig, len(s.configs))
	maps.Copy(configs, s.configs)
	s.mu.RUnlock()
	return SanitizeProviderConfigs(configs)
}
