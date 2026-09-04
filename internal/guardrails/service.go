package guardrails

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// Summarizer is implemented by plugins that render a one-line summary of a
// config for the guardrails list.
type Summarizer interface {
	Summarize(config json.RawMessage) string
}

// Normalizer is implemented by plugins that canonicalize a validated config
// before it is stored (for example folding a provider hint into the model).
type Normalizer interface {
	Normalize(config json.RawMessage) (json.RawMessage, error)
}

type serviceSnapshot struct {
	definitions map[string]Definition
	order       []string
	instances   map[string]*plugins.Instance
	summaries   map[string]string
	// keys identifies what each instance was built from, so a refresh can
	// keep instances whose definition did not change.
	keys map[string]string
}

// defaultRetireAfter is how long a replaced instance stays open after the
// swap: compiled workflows are rebuilt on their own refresh and in-flight
// requests finish in the meantime.
const defaultRetireAfter = 2 * time.Minute

// retiredInstance is an instance a swap replaced, waiting to be closed.
type retiredInstance struct {
	inst *plugins.Instance
	at   time.Time
}

// Service keeps reusable guardrail instances cached in memory and refreshes
// them from storage. Instances whose definition is unchanged survive a
// refresh; replaced ones are closed once compiled workflows have moved on.
type Service struct {
	store   Store
	catalog *plugins.Catalog
	deps    plugins.HostDeps
	chat    *chatCompleterRef

	refreshMu sync.Mutex
	mu        sync.RWMutex
	snapshot  serviceSnapshot
	retired   []retiredInstance
	// retireAfter is the delay before a replaced instance is closed.
	retireAfter time.Duration
	// now is the clock; tests replace it.
	now func() time.Time
}

// chatCompleterRef lets the internal executor be swapped after instances are
// built without rebuilding them.
type chatCompleterRef struct {
	mu   sync.RWMutex
	chat plugins.ChatCompleter
}

func (r *chatCompleterRef) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	r.mu.RLock()
	chat := r.chat
	r.mu.RUnlock()
	if chat == nil {
		return nil, plugins.ErrInferenceUnavailable
	}
	return chat.ChatCompletion(ctx, req)
}

// NewService creates a guardrail service backed by the provided store. The
// catalog supplies the plugin types instances are built from; deps are the
// gateway services exposed to plugins.
func NewService(store Store, catalog *plugins.Catalog, deps plugins.HostDeps) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("plugin catalog is required")
	}
	chat := &chatCompleterRef{chat: deps.Chat}
	deps.Chat = chat
	return &Service{
		store:       store,
		catalog:     catalog,
		deps:        deps,
		chat:        chat,
		snapshot:    emptySnapshot(),
		retireAfter: defaultRetireAfter,
		now:         time.Now,
	}, nil
}

func emptySnapshot() serviceSnapshot {
	return serviceSnapshot{
		definitions: map[string]Definition{},
		order:       []string{},
		instances:   map[string]*plugins.Instance{},
		summaries:   map[string]string{},
		keys:        map[string]string{},
	}
}

// SetChatCompleter swaps the gateway-internal chat executor plugins use for
// inference. Instances see the new executor on their next call.
func (s *Service) SetChatCompleter(chat plugins.ChatCompleter) {
	if s == nil {
		return
	}
	s.chat.mu.Lock()
	s.chat.chat = chat
	s.chat.mu.Unlock()
}

// Refresh reloads guardrails from storage and atomically swaps the in-memory snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) error {
	definitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	next, err := s.buildSnapshot(ctx, definitions)
	if err != nil {
		return guardrailServiceError("load guardrails", err)
	}
	s.swap(ctx, next)
	return nil
}

// swap installs next, retires the instances it replaced, and closes retired
// instances whose grace period has passed.
func (s *Service) swap(ctx context.Context, next serviceSnapshot) {
	now := s.now()
	s.mu.Lock()
	previous := s.snapshot
	s.snapshot = next
	for name, inst := range previous.instances {
		if next.instances[name] != inst {
			s.retired = append(s.retired, retiredInstance{inst: inst, at: now})
		}
	}
	var due []*plugins.Instance
	kept := s.retired[:0]
	for _, r := range s.retired {
		if now.Sub(r.at) >= s.retireAfter {
			due = append(due, r.inst)
			continue
		}
		kept = append(kept, r)
	}
	s.retired = kept
	s.mu.Unlock()
	_ = closeInstances(ctx, due) // failures are logged
}

// discard closes the instances of a snapshot that never served: those built
// for it rather than reused from the current one.
func (s *Service) discard(ctx context.Context, next serviceSnapshot) {
	s.mu.RLock()
	current := s.snapshot.instances
	s.mu.RUnlock()
	var fresh []*plugins.Instance
	for name, inst := range next.instances {
		if current[name] != inst {
			fresh = append(fresh, inst)
		}
	}
	_ = closeInstances(ctx, fresh) // failures are logged
}

// Close closes every active and retired instance. The service keeps an empty
// snapshot afterwards; call it after request handling and refreshes stopped.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	var all []*plugins.Instance
	for _, inst := range s.snapshot.instances {
		all = append(all, inst)
	}
	for _, r := range s.retired {
		all = append(all, r.inst)
	}
	s.snapshot = emptySnapshot()
	s.retired = nil
	s.mu.Unlock()
	return closeInstances(ctx, all)
}

func closeInstances(ctx context.Context, instances []*plugins.Instance) error {
	var errs []error
	for _, inst := range instances {
		if err := inst.Close(ctx); err != nil {
			slog.Warn("guardrail instance close failed", "instance", inst.Name, "plugin", inst.Type, "error", err)
			errs = append(errs, fmt.Errorf("close %q: %w", inst.Name, err))
		}
	}
	return errors.Join(errs...)
}

// UpsertDefinitions validates and upserts a definition set (configuration
// seeding), then swaps the snapshot on success. Secrets are stored as given.
func (s *Service) UpsertDefinitions(ctx context.Context, definitions []Definition) error {
	if s == nil || len(definitions) == 0 {
		return nil
	}
	normalized := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		normalizedDefinition, err := s.normalizeDefinition(definition)
		if err != nil {
			return err
		}
		normalized = append(normalized, normalizedDefinition)
	}
	return s.commit(ctx, func(next map[string]Definition) error {
		for _, definition := range normalized {
			next[definition.Name] = definition
		}
		return nil
	}, func() error { return s.store.UpsertMany(ctx, normalized) }, "upsert guardrails")
}

// Upsert validates and stores a guardrail definition, then swaps the
// snapshot on success. A masked secret keeps the stored value.
func (s *Service) Upsert(ctx context.Context, definition Definition) error {
	identity, err := normalizeDefinitionIdentity(definition)
	if err != nil {
		return err
	}
	if stored, ok := s.Get(identity.Name); ok && stored.Type == identity.Type {
		if entry, ok := s.catalog.Lookup(identity.Type); ok {
			identity.Config = plugins.MergeSecrets(entry.Manifest.ConfigSchema, identity.Config, s.storedConfig(identity.Name))
		}
	}
	normalized, err := s.normalizeDefinition(identity)
	if err != nil {
		return err
	}
	return s.commit(ctx, func(next map[string]Definition) error {
		next[normalized.Name] = normalized
		return nil
	}, func() error { return s.store.Upsert(ctx, normalized) }, "upsert guardrail")
}

// Delete removes a guardrail definition from storage and swaps the snapshot on success.
func (s *Service) Delete(ctx context.Context, name string) error {
	name = normalizeDefinitionName(name)
	if name == "" {
		return newValidationError("guardrail name is required", nil)
	}
	return s.commit(ctx, func(next map[string]Definition) error {
		delete(next, name)
		return nil
	}, func() error { return s.store.Delete(ctx, name) }, "delete guardrail")
}

// commit applies mutate to the stored definition set, builds the resulting
// snapshot (so invalid input never reaches storage), persists, then swaps.
func (s *Service) commit(ctx context.Context, mutate func(map[string]Definition) error, persist func() error, action string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	currentDefinitions, err := s.store.List(ctx)
	if err != nil {
		return guardrailServiceError("list guardrails", err)
	}
	nextDefinitions := definitionMap(currentDefinitions)
	if err := mutate(nextDefinitions); err != nil {
		return err
	}
	next, err := s.buildSnapshot(ctx, definitionsFromMap(nextDefinitions))
	if err != nil {
		return err
	}
	if err := persist(); err != nil {
		s.discard(ctx, next)
		return guardrailServiceError(action, err)
	}
	s.swap(ctx, next)
	return nil
}

// List returns all cached guardrail definitions sorted by name, secrets
// redacted.
func (s *Service) List() []Definition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Definition, 0, len(s.snapshot.order))
	for _, name := range s.snapshot.order {
		result = append(result, s.redacted(s.snapshot.definitions[name]))
	}
	return result
}

// ListViews returns all cached guardrail definitions with phases and summaries.
func (s *Service) ListViews() []View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]View, 0, len(s.snapshot.order))
	for _, name := range s.snapshot.order {
		views = append(views, s.viewLocked(name))
	}
	return views
}

// Get returns one cached guardrail by name, secrets redacted.
func (s *Service) Get(name string) (*Definition, bool) {
	name = normalizeDefinitionName(name)
	if name == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.snapshot.definitions[name]
	if !ok {
		return nil, false
	}
	redacted := s.redacted(definition)
	return &redacted, true
}

// GetView returns one cached guardrail view by name.
func (s *Service) GetView(name string) (View, bool) {
	name = normalizeDefinitionName(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.snapshot.definitions[name]; !ok {
		return View{}, false
	}
	return s.viewLocked(name), true
}

func (s *Service) viewLocked(name string) View {
	view := View{Definition: s.redacted(s.snapshot.definitions[name]), Summary: s.snapshot.summaries[name]}
	if inst := s.snapshot.instances[name]; inst != nil {
		view.Phases = phaseNames(inst.Kinds)
	}
	return view
}

func (s *Service) redacted(def Definition) Definition {
	cloned := cloneDefinition(def)
	if entry, ok := s.catalog.Lookup(def.Type); ok {
		cloned.Config = plugins.RedactSecrets(entry.Manifest.ConfigSchema, cloned.Config)
	}
	return cloned
}

func (s *Service) storedConfig(name string) json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.definitions[name].Config
}

// InstanceConfig returns the stored (unredacted) config and type of one
// cached guardrail, for building plugin instances outside this service.
// Callers must not expose the config to admin clients.
func (s *Service) InstanceConfig(name string) (config json.RawMessage, pluginType string, ok bool) {
	name = normalizeDefinitionName(name)
	if s == nil || name == "" {
		return nil, "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.snapshot.definitions[name]
	if !ok {
		return nil, "", false
	}
	return append(json.RawMessage(nil), definition.Config...), definition.Type, true
}

// TypeDefinitions returns the guardrail editor schema of every catalog plugin
// that implements a prompt, response, or stream hook.
func (s *Service) TypeDefinitions() []TypeDefinition {
	defs := []TypeDefinition{}
	for _, entry := range s.catalog.Entries() {
		if entry.Err != nil || !hasPhase(entry.Kinds) {
			continue
		}
		defs = append(defs, typeDefinitionFromEntry(entry))
	}
	return defs
}

// Len returns the number of loaded guardrails.
func (s *Service) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.order)
}

// Names returns the loaded guardrail names in sorted order.
func (s *Service) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.snapshot.order...)
}

// BuildChains resolves named steps into per-phase chains through the current
// in-memory instances.
func (s *Service) BuildChains(steps []StepReference) (*plugins.Chains, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	instances := s.snapshot.instances
	s.mu.RUnlock()
	if instances == nil {
		return nil, core.NewProviderError("", http.StatusBadGateway, "guardrail catalog is not loaded", nil)
	}
	return buildChains(instances, steps)
}

func buildChains(instances map[string]*plugins.Instance, steps []StepReference) (*plugins.Chains, error) {
	refs := map[pluginapi.Kind][]plugins.Ref{}
	for _, step := range steps {
		name := normalizeDefinitionName(step.Ref)
		if name == "" {
			return nil, fmt.Errorf("guardrail ref is required")
		}
		phase := step.Phase
		if phase == "" {
			phase = pluginapi.KindPrompt
		}
		if !plugins.IsPhaseKind(phase) {
			return nil, fmt.Errorf("unknown guardrail phase %q for ref %q", phase, name)
		}
		inst, ok := instances[name]
		if !ok {
			return nil, fmt.Errorf("unknown guardrail ref: %s", name)
		}
		if !inst.HasKind(phase) {
			return nil, fmt.Errorf("guardrail %q (%s) does not support the %s phase", name, inst.Type, phase)
		}
		refs[phase] = append(refs[phase], plugins.Ref{Instance: inst, Step: step.Step})
	}
	chains := &plugins.Chains{}
	var err error
	if chains.Prompt, err = plugins.BuildChain(pluginapi.KindPrompt, refs[pluginapi.KindPrompt]); err != nil {
		return nil, err
	}
	if chains.Response, err = plugins.BuildChain(pluginapi.KindResponse, refs[pluginapi.KindResponse]); err != nil {
		return nil, err
	}
	if chains.Stream, err = plugins.BuildChain(pluginapi.KindStream, refs[pluginapi.KindStream]); err != nil {
		return nil, err
	}
	return chains, nil
}

// normalizeDefinition validates identity and config against the catalog.
func (s *Service) normalizeDefinition(def Definition) (Definition, error) {
	def, err := normalizeDefinitionIdentity(def)
	if err != nil {
		return Definition{}, err
	}
	entry, ok := s.catalog.Lookup(def.Type)
	if !ok || !hasPhase(entry.Kinds) {
		return Definition{}, newValidationError(`unknown guardrail type: "`+def.Type+`"`, nil)
	}
	config, err := plugins.ValidateConfig(entry.Manifest.ConfigSchema, def.Config, pluginapi.ScopeInstance)
	if err != nil {
		return Definition{}, newValidationError("invalid "+def.Type+" config: "+err.Error(), err)
	}
	if normalizer, ok := entry.Factory().(Normalizer); ok {
		normalized, err := normalize(normalizer, config)
		if err != nil {
			return Definition{}, newValidationError("invalid "+def.Type+" config: "+err.Error(), err)
		}
		if config, err = plugins.ValidateConfig(entry.Manifest.ConfigSchema, normalized, pluginapi.ScopeInstance); err != nil {
			return Definition{}, newValidationError("invalid "+def.Type+" config: "+err.Error(), err)
		}
	}
	def.Config = config
	return def, nil
}

// normalize calls a plugin's Normalize with panic recovery.
func normalize(normalizer Normalizer, config json.RawMessage) (normalized json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			normalized, err = nil, fmt.Errorf("plugin panicked while normalizing the config: %v", r)
		}
	}()
	return normalizer.Normalize(config)
}

// buildSnapshot builds the snapshot for definitions, reusing every current
// instance whose definition is unchanged (see instanceKey). On error the
// instances built so far are closed.
func (s *Service) buildSnapshot(ctx context.Context, definitions []Definition) (next serviceSnapshot, err error) {
	s.mu.RLock()
	current := s.snapshot
	s.mu.RUnlock()
	next = emptySnapshot()
	var built []*plugins.Instance
	defer func() {
		if err != nil {
			_ = closeInstances(ctx, built) // failures are logged
		}
	}()
	perType := map[string]int{}
	for _, definition := range definitions {
		normalized, err := s.normalizeDefinition(definition)
		if err != nil {
			return serviceSnapshot{}, fmt.Errorf("load guardrail %q: %w", definition.Name, err)
		}
		if _, dup := next.definitions[normalized.Name]; dup {
			return serviceSnapshot{}, fmt.Errorf("duplicate guardrail definition %q", normalized.Name)
		}
		entry, _ := s.catalog.Lookup(normalized.Type)
		perType[entry.Name]++
		if entry.SingleInstance && perType[entry.Name] > 1 {
			return serviceSnapshot{}, fmt.Errorf("load guardrail %q: plugin %q (%s) supports a single configured instance", normalized.Name, entry.Name, entry.Source)
		}
		key := instanceKey(normalized)
		inst := current.instances[normalized.Name]
		if inst == nil || current.keys[normalized.Name] != key {
			host := plugins.NewHost(s.deps, plugins.HostInfo{PluginName: entry.Name, InstanceName: normalized.Name, UserPath: normalized.UserPath})
			inst, err = plugins.NewInstance(ctx, entry, instanceSpec(normalized), host)
			if err != nil {
				return serviceSnapshot{}, newValidationError(fmt.Sprintf("load guardrail %q: %v", normalized.Name, err), err)
			}
			built = append(built, inst)
		}
		next.definitions[normalized.Name] = normalized
		next.instances[normalized.Name] = inst
		next.keys[normalized.Name] = key
		next.summaries[normalized.Name] = summarize(inst.Plugin, normalized.Config)
		next.order = append(next.order, normalized.Name)
	}
	sort.Strings(next.order)
	return next, nil
}

// instanceKey captures everything an instance is built from; a definition
// change outside these fields (say the description) keeps the instance.
func instanceKey(def Definition) string {
	return def.Type + "\x00" + def.UserPath + "\x00" + def.FailMode + "\x00" + strconv.Itoa(def.TimeoutMS) + "\x00" + string(def.Config)
}

func summarize(plugin pluginapi.Plugin, config json.RawMessage) (summary string) {
	summarizer, ok := plugin.(Summarizer)
	if !ok {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			summary = ""
		}
	}()
	return summarizer.Summarize(config)
}

func definitionMap(definitions []Definition) map[string]Definition {
	next := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		next[definition.Name] = cloneDefinition(definition)
	}
	return next
}

func definitionsFromMap(definitions map[string]Definition) []Definition {
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func guardrailServiceError(message string, err error) error {
	if err == nil {
		return nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}
	if IsValidationError(err) {
		return core.NewInvalidRequestError(message+": "+err.Error(), err)
	}
	return core.NewProviderError("", http.StatusBadGateway, message+": "+err.Error(), err)
}
