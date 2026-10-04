package mcpgateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// memStore is an in-memory Store.
type memStore struct {
	mu   sync.Mutex
	rows map[string]ManagedServer
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

	_, running := service.manager.spec("broken")
	assert.False(t, running, "a row whose references fail at startup is skipped")
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
