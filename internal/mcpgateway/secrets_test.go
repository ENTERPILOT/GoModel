package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/encryption"
)

// memStore is an in-memory Store.
type memStore struct {
	mu      sync.Mutex
	rows    map[string]ManagedServer
	listErr error
}

func newMemStore(rows ...ManagedServer) *memStore {
	s := &memStore{rows: map[string]ManagedServer{}}
	for _, row := range rows {
		s.rows[row.Name] = row
	}
	return s
}

func (s *memStore) List(context.Context) ([]ManagedServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	rows := make([]ManagedServer, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *memStore) Get(_ context.Context, name string) (*ManagedServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &row, nil
}

func (s *memStore) Upsert(_ context.Context, server ManagedServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[server.Name] = server
	return nil
}

func (s *memStore) Update(_ context.Context, server ManagedServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[server.Name]; !ok {
		return ErrNotFound
	}
	s.rows[server.Name] = server
	return nil
}

func (s *memStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[name]; !ok {
		return ErrNotFound
	}
	delete(s.rows, name)
	return nil
}

func (s *memStore) Close() error { return nil }

// mapVault resolves ${vault:...} from a mutable map.
type mapVault struct {
	mu     sync.Mutex
	values map[string]string
	fields []string
}

func (v *mapVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *mapVault) ResolveSecret(ctx context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	field, _ := config.SecretFieldFromContext(ctx)
	v.fields = append(v.fields, field)
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

// headerWriter stores secrets under ${vault:written/...} references.
type headerWriter struct {
	vault   *mapVault
	deleted []string
}

func (w *headerWriter) WriteSecret(_ context.Context, key config.SecretKey, value string) (string, error) {
	reference := "written/" + key.ID + "/" + key.Field
	w.vault.set(reference, value)
	return "${vault:" + reference + "}", nil
}

func (w *headerWriter) DeleteSecret(_ context.Context, reference string) error {
	w.deleted = append(w.deleted, reference)
	return nil
}

func (w *headerWriter) OwnsReference(reference string) bool {
	return strings.HasPrefix(reference, "${vault:written/")
}

// disabledServer is an admin-managed row the manager never dials.
func disabledServer(name string, headers map[string]string) ManagedServer {
	return ManagedServer{Name: name, DisplayName: name, URL: "https://" + name + ".invalid/mcp", Transport: config.MCPTransportHTTP, Headers: headers}
}

func newSecretsTestService(t *testing.T, store Store, vault *mapVault) (*Service, *config.Secrets) {
	t.Helper()
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	service, err := NewService(t.Context(), Options{Store: store, Secrets: secrets})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	return service, secrets
}

func viewSpec(t *testing.T, service *Service, name string) ServerSpec {
	t.Helper()
	spec, ok := service.manager.spec(name)
	require.True(t, ok, "server %s is not running", name)
	return spec
}

func TestServiceResolvesHeaderReferences(t *testing.T) {
	vault := &mapVault{values: map[string]string{"gh": "ghp_1"}}
	store := newMemStore(
		disabledServer("github", map[string]string{"Authorization": "Bearer ${vault:gh}", "X-Region": "eu"}),
		disabledServer("broken", map[string]string{"Authorization": "${vault:gone}"}),
	)
	service, _ := newSecretsTestService(t, store, vault)

	spec := viewSpec(t, service, "github")
	assert.Equal(t, map[string]string{"Authorization": "Bearer ghp_1", "X-Region": "eu"}, spec.Headers)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${vault:gh}"}, spec.HeaderReferences)
	assert.Contains(t, vault.fields, "mcp_servers.github.headers.Authorization")

	assert.False(t, service.Running("broken"), "a row whose references fail at startup is skipped")
	assert.False(t, service.Running("github"), "a disabled server serves nothing, so it never blocks a reload")
	skipped := service.Skipped()
	require.Len(t, skipped, 1)
	assert.ErrorContains(t, skipped["broken"], "mcp_servers.broken.headers.Authorization")
}

func TestServiceUpsertRejectsUnresolvableReference(t *testing.T) {
	vault := &mapVault{values: map[string]string{}}
	store := newMemStore()
	service, _ := newSecretsTestService(t, store, vault)

	err := service.Upsert(t.Context(), disabledServer("github", map[string]string{"Authorization": "Bearer ${vault:missing}"}))
	secretErr, ok := errors.AsType[*config.SecretError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "mcp_servers.github.headers.Authorization", secretErr.Field)
	assert.Empty(t, store.rows)
}

func TestServiceRotateSecretsRedialsOnlyTheAffectedServer(t *testing.T) {
	vault := &mapVault{values: map[string]string{"gh": "ghp_1", "other": "o1"}}
	store := newMemStore(
		disabledServer("github", map[string]string{"Authorization": "Bearer ${vault:gh}"}),
		disabledServer("other", map[string]string{"Authorization": "${vault:other}"}),
	)
	service, secrets := newSecretsTestService(t, store, vault)
	ctx := t.Context()
	githubBefore, _ := service.manager.get("github")
	otherBefore, _ := service.manager.get("other")

	vault.set("gh", "ghp_2")
	vault.set("other", "o2") // changed too, but not reported for this rotation
	err := service.RotateSecrets(ctx, []string{"mcp_servers.github.headers.Authorization"})
	require.NoError(t, err)

	githubAfter, _ := service.manager.get("github")
	otherAfter, _ := service.manager.get("other")
	assert.NotSame(t, githubBefore, githubAfter, "the rotated server is redialed")
	assert.Same(t, otherBefore, otherAfter, "other servers keep their session")
	assert.Equal(t, "Bearer ghp_2", viewSpec(t, service, "github").Headers["Authorization"])

	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp_servers.other.headers.Authorization"}, recheck.Fields())

	// A failed lookup keeps the running headers.
	delete(vault.values, "gh")
	require.Error(t, service.RotateSecrets(ctx, []string{"mcp_servers.github.headers.Authorization"}))
	assert.Equal(t, "Bearer ghp_2", viewSpec(t, service, "github").Headers["Authorization"])
}

func TestServiceSecretWriter(t *testing.T) {
	vault := &mapVault{values: map[string]string{"hand": "h"}}
	store := newMemStore()
	service, secrets := newSecretsTestService(t, store, vault)
	writer := &headerWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()

	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "Bearer typed", "X-Hand": "${vault:hand}"})))
	assert.Equal(t, map[string]string{
		"Authorization": "${vault:written/github/headers.Authorization}",
		"X-Hand":        "${vault:hand}",
	}, store.rows["github"].Headers)
	assert.Equal(t, "Bearer typed", viewSpec(t, service, "github").Headers["Authorization"])

	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"X-Hand": "${vault:hand}"})))
	assert.Equal(t, []string{"${vault:written/github/headers.Authorization}"}, writer.deleted)

	writer.deleted = nil
	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "again"})))
	require.NoError(t, service.Delete(ctx, "github"))
	assert.Equal(t, []string{"${vault:written/github/headers.Authorization}"}, writer.deleted)

	vault.set("written/github/headers.Authorization", "rotated")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields(), "a deleted server is no longer watched")
}

// The replaced headers are released once the new row is stored, even when
// applying it to the running set fails afterwards.
func TestServiceUpsertReleasesReplacedSecretsWhenApplyFails(t *testing.T) {
	vault := &mapVault{values: map[string]string{}}
	store := newMemStore()
	service, secrets := newSecretsTestService(t, store, vault)
	writer := &headerWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()

	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "first"})))
	store.mu.Lock()
	store.listErr = errors.New("store unavailable")
	store.mu.Unlock()

	err := service.Upsert(ctx, disabledServer("github", map[string]string{"X-Other": "${env:HOME}"}))
	require.ErrorContains(t, err, "saved but not applied")
	assert.Equal(t, []string{"${vault:written/github/headers.Authorization}"}, writer.deleted)
}

// A writer-owned reference is only saved to the server whose stored row holds
// it: one read from a row since replaced or deleted, or copied from another
// server, may name a secret that was already released.
func TestServiceUpsertRejectsWriterReferencesTheRowDoesNotHold(t *testing.T) {
	vault := &mapVault{values: map[string]string{}}
	store := newMemStore()
	service, secrets := newSecretsTestService(t, store, vault)
	writer := &headerWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	owned := "${vault:written/github/headers.Authorization}"

	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "typed"})))
	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": owned, "X-Region": "eu"})), "the stored row holds it")

	err := service.Upsert(ctx, disabledServer("copy", map[string]string{"Authorization": owned}))
	require.ErrorIs(t, err, config.ErrSecretNotHeld)
	secretErr, ok := errors.AsType[*config.SecretError](err)
	require.True(t, ok)
	assert.Equal(t, "mcp_servers.copy.headers.Authorization", secretErr.Field)
	assert.NotContains(t, store.rows, "copy")

	require.NoError(t, service.Delete(ctx, "github"))
	writer.deleted = nil
	err = service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": owned}))
	require.ErrorIs(t, err, config.ErrSecretNotHeld, "the stale form of a deleted server is not saved")
	assert.NotContains(t, store.rows, "github")
	assert.Empty(t, writer.deleted)
}

// gatedDeleteStore blocks Delete until release is closed.
type gatedDeleteStore struct {
	*memStore
	deleting chan struct{}
	release  chan struct{}
}

func (s *gatedDeleteStore) Delete(ctx context.Context, name string) error {
	close(s.deleting)
	<-s.release
	return s.memStore.Delete(ctx, name)
}

// countingWriter returns a distinct reference per write.
type countingWriter struct {
	headerWriter
	mu     sync.Mutex
	writes int
}

func (w *countingWriter) WriteSecret(_ context.Context, key config.SecretKey, value string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	reference := fmt.Sprintf("written/%s/%s@%d", key.ID, key.Field, w.writes)
	w.vault.set(reference, value)
	return "${vault:" + reference + "}", nil
}

func (w *countingWriter) DeleteSecret(_ context.Context, reference string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deleted = append(w.deleted, reference)
	return nil
}

// A save racing a delete never leaves a stored row pointing at a released
// secret, nor a written secret that no row holds.
func TestServiceSerializesSavesAndDeletesThroughSecretCleanup(t *testing.T) {
	vault := &mapVault{values: map[string]string{}}
	store := &gatedDeleteStore{memStore: newMemStore(), deleting: make(chan struct{}), release: make(chan struct{})}
	service, secrets := newSecretsTestService(t, store, vault)
	writer := &countingWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "first"})))

	deleted := make(chan error, 1)
	go func() { deleted <- service.Delete(ctx, "github") }()
	<-store.deleting

	saved := make(chan error, 1)
	go func() {
		saved <- service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "second"}))
	}()
	select {
	case err := <-saved:
		// Only reachable without serialization; the checks below then fail.
		saved <- err
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	require.NoError(t, <-deleted)
	require.NoError(t, <-saved)

	row, ok := store.rows["github"]
	require.True(t, ok, "the save follows the delete")
	assert.Equal(t, "${vault:written/github/headers.Authorization@2}", row.Headers["Authorization"])
	writer.mu.Lock()
	defer writer.mu.Unlock()
	assert.Equal(t, []string{"${vault:written/github/headers.Authorization@1}"}, writer.deleted)
}

// sealUnconfirmedMemStore reports every save, once unconfirmed is set, as
// written with an unconfirmed data key.
type sealUnconfirmedMemStore struct {
	*memStore
	unconfirmed bool
}

func (s *sealUnconfirmedMemStore) Upsert(ctx context.Context, server ManagedServer) error {
	if err := s.memStore.Upsert(ctx, server); err != nil {
		return err
	}
	if s.unconfirmed {
		return encryption.ErrSealUnconfirmed
	}
	return nil
}

// A save whose data key is unconfirmed was written: it is applied, the
// secrets it replaced are released, and the ones it wrote are kept.
func TestServiceUnconfirmedSaveKeepsWhatItWrote(t *testing.T) {
	vault := &mapVault{values: map[string]string{"hand": "h"}}
	store := &sealUnconfirmedMemStore{memStore: newMemStore()}
	service, secrets := newSecretsTestService(t, store, vault)
	writer := &headerWriter{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	written := "${vault:written/github/headers.Authorization}"

	require.NoError(t, service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "typed"})))
	store.unconfirmed = true

	err := service.Upsert(ctx, disabledServer("github", map[string]string{"X-Hand": "${vault:hand}"}))
	require.ErrorIs(t, err, encryption.ErrSealUnconfirmed)
	assert.Equal(t, []string{written}, writer.deleted, "the replaced secret is released")
	assert.Equal(t, "h", viewSpec(t, service, "github").Headers["X-Hand"], "the saved row is applied")

	writer.deleted = nil
	err = service.Upsert(ctx, disabledServer("github", map[string]string{"Authorization": "again"}))
	require.ErrorIs(t, err, encryption.ErrSealUnconfirmed)
	assert.Empty(t, writer.deleted, "the secret the stored row now holds is kept")
	assert.Equal(t, map[string]string{"Authorization": written}, store.rows["github"].Headers)
	assert.Equal(t, "again", viewSpec(t, service, "github").Headers["Authorization"])
}

// A new server is refused a virtual server's name before any secret is
// written; a row stored under that name before stays editable.
func TestServiceVirtualNameIsRefusedBeforeSecretsAreWritten(t *testing.T) {
	vault := &mapVault{values: map[string]string{}}
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	secrets.SetWriter(&headerWriter{vault: vault})
	store := newMemStore(disabledServer("legacy", nil))
	service, err := NewService(t.Context(), Options{
		Store:   store,
		Secrets: secrets,
		VirtualServers: map[string]VirtualServerSpec{
			"coding": {Name: "coding", Servers: []string{"legacy"}},
			"legacy": {Name: "legacy", Servers: []string{"coding"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	ctx := t.Context()

	err = service.Upsert(ctx, disabledServer("coding", map[string]string{"Authorization": "typed"}))
	require.ErrorIs(t, err, ErrVirtualNameTaken)
	assert.NotContains(t, store.rows, "coding")
	assert.Empty(t, vault.values, "nothing is written for a refused name")

	require.NoError(t, service.Upsert(ctx, disabledServer("legacy", map[string]string{"Authorization": "typed"})))
	assert.Equal(t, map[string]string{"Authorization": "${vault:written/legacy/headers.Authorization}"}, store.rows["legacy"].Headers)
}
