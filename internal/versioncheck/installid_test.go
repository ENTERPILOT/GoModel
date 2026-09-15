package versioncheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTempDataDir points platformdir's project-local data directory at a fresh
// temp dir, so each test starts without an install-id file.
func useTempDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.Mkdir(filepath.Join(dir, "data"), 0o755)
	require.NoError(t, err)

	t.Chdir(dir)
	return filepath.Join(dir, "data", installIDFile)
}

func writeFile(t *testing.T, path, id string) {
	t.Helper()
	err := os.WriteFile(path, []byte(id+"\n"), 0o600)
	require.NoError(t, err)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// fakeStore is an in-memory Store whose failures can be switched on and off
// mid-test, standing in for a database that comes and goes.
type fakeStore struct {
	mu     sync.Mutex
	values map[string]string
	getErr error
	setErr error
	sets   int
}

func newFakeStore() *fakeStore { return &fakeStore{values: map[string]string{}} }

func (s *fakeStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", false, s.getErr
	}
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *fakeStore) SetDefault(_ context.Context, key, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return "", s.setErr
	}
	if existing, ok := s.values[key]; ok {
		return existing, nil
	}
	s.values[key] = value
	return value, nil
}

func (s *fakeStore) fail(get, set error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getErr, s.setErr = get, set
}

func (s *fakeStore) value(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

const (
	existingID = "3f2a0000-0000-4000-8000-000000000001"
	freshID    = "ffffffff-0000-4000-8000-0000000000ff"
)

// resolveInstallID resolves once with a fresh Identity, the way a gateway
// start does.
func resolveInstallID(ctx context.Context, store Store, secret string) (string, InstallIDSource) {
	return NewIdentity(store, secret).Resolve(ctx)
}

func TestResolveInstallIDGeneratesAndPersistsEverywhere(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()

	id, source := resolveInstallID(context.Background(), store, "")

	require.Equal(t, SourceGenerated, source)
	_, err := uuid.Parse(id)
	require.NoError(t, err, "id %q is not a UUID: %v", id, err)
	got := store.value(InstallIDKey)
	assert.Equal(t, id, got)
	got = readFile(t, path)
	assert.Equal(t, id+"\n", got)

	again, source := resolveInstallID(context.Background(), store, "")
	assert.Equal(t, id, again)
	assert.Equal(t, SourceDatabase, source)
}

func TestResolveInstallIDMigratesExistingFileToDatabaseUnchanged(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, existingID)
	store := newFakeStore()

	id, source := resolveInstallID(context.Background(), store, "secret")

	require.Equal(t, existingID, id)
	require.Equal(t, SourceFile, source)
	got := store.value(InstallIDKey)
	assert.Equal(t, id, got)
}

func TestResolveInstallIDDatabaseWinsOverRegeneratedFile(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, freshID)
	store := newFakeStore()
	store.values[InstallIDKey] = existingID

	id, source := resolveInstallID(context.Background(), store, "")

	require.Equal(t, existingID, id)
	require.Equal(t, SourceDatabase, source)
	got := readFile(t, path)
	assert.Equal(t, id+"\n", got)
}

func TestResolveInstallIDKeepsFileWhenDatabaseErrors(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, existingID)
	store := newFakeStore()
	store.fail(errors.New("connection refused"), nil)

	id, source := resolveInstallID(context.Background(), store, "secret")

	require.Equal(t, existingID, id)
	require.Equal(t, SourceFile, source)
	assert.Equal(t, 0, store.sets)
}

func TestResolveInstallIDKeepsFileWhenDatabaseWriteFails(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()
	store.fail(nil, errors.New("read-only transaction"))

	id, source := resolveInstallID(context.Background(), store, "")
	require.Equal(t, SourceGenerated, source)
	got := readFile(t, path)
	require.Equal(t, id+"\n", got, "file holds %q after a failed database write, want %q", got, id)

	// The file is what survives; a later start without the database reads it.
	again, source := resolveInstallID(context.Background(), nil, "")
	assert.Equal(t, id, again)
	assert.Equal(t, SourceFile, source)
}

func TestResolveInstallIDNeverAdoptsBlankDatabaseValue(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	// Pathological: something left a blank value under the key. SetDefault
	// keeps it (insert-if-absent), so its read-back returns the blank; the
	// resolved id must be the local candidate, never the blank.
	store.values[InstallIDKey] = " "

	id, source := resolveInstallID(context.Background(), store, "secret")

	require.NotEmpty(t, id)
	require.NotEqual(t, " ", id)
	require.Equal(t, SourceDerived, source)
}

func TestIdentityRecoversDatabaseIDAfterOutage(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	store.values[InstallIDKey] = existingID
	store.fail(errors.New("connection refused"), errors.New("connection refused"))
	identity := NewIdentity(store, "secret")

	// No file, database down: a provisional id is the best available, and it
	// must stay the same provisional id while the outage lasts.
	provisional, source := identity.Resolve(context.Background())
	require.Equal(t, SourceDerived, source)
	require.NotEqual(t, existingID, provisional)
	again := identity.ID(context.Background())
	require.Equal(t, provisional, again)

	store.fail(nil, nil)
	id, source := identity.Resolve(context.Background())
	require.Equal(t, existingID, id)
	require.Equal(t, SourceDatabase, source)
	got := store.value(InstallIDKey)
	assert.Equal(t, existingID, got)
}

func TestIdentityConvergesConcurrentFirstStarts(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()

	// Replicas share a database but not a data directory, so each has its
	// own Identity and no file; every one of them must end up with the id
	// the database kept.
	const replicas = 16
	ids := make([]string, replicas)
	var wg sync.WaitGroup
	for r := range replicas {
		wg.Go(func() {
			ids[r] = NewIdentity(store, "").ID(context.Background())
		})
	}
	wg.Wait()

	winner := store.value(InstallIDKey)
	for r, id := range ids {
		assert.Equal(t, winner, id, "replica %d kept %q, database holds %q", r, id, winner)
	}
}

func TestResolveInstallIDDerivesFromSecretWhenNothingStored(t *testing.T) {
	useTempDataDir(t)

	first, source := resolveInstallID(context.Background(), nil, "operator-secret")
	require.Equal(t, SourceDerived, source)
	_, err := uuid.Parse(first)
	require.NoError(t, err, "derived id %q is not a UUID: %v", first, err)

	// A recreated container: no file, same secret, same id.
	useTempDataDir(t)
	second, _ := resolveInstallID(context.Background(), nil, "operator-secret")
	assert.Equal(t, first, second)

	useTempDataDir(t)
	other, _ := resolveInstallID(context.Background(), nil, "another-secret")
	assert.NotEqual(t, first, other)
}

func TestResolveInstallIDWithoutStoreUsesFile(t *testing.T) {
	path := useTempDataDir(t)

	id, source := resolveInstallID(context.Background(), nil, "")
	require.Equal(t, SourceGenerated, source)
	got := readFile(t, path)
	require.Equal(t, id+"\n", got, "file holds %q, want %q", got, id)

	again, source := resolveInstallID(context.Background(), nil, "")
	assert.Equal(t, id, again)
	assert.Equal(t, SourceFile, source)
}
