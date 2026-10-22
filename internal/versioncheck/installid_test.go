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

func (s *fakeStore) Replace(_ context.Context, key, old, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return "", s.setErr
	}
	if s.values[key] == old {
		s.values[key] = value
	}
	return s.values[key], nil
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
	// derivedID has the version-8 shape of an id that earlier releases
	// derived from the master key.
	derivedID = "3f2a0000-0000-8000-8000-000000000002"
)

// resolveInstallID resolves once with a fresh Identity, the way a gateway
// start does.
func resolveInstallID(ctx context.Context, store Store) (string, InstallIDSource) {
	return NewIdentity(store).Resolve(ctx)
}

func TestResolveInstallIDGeneratesAndPersistsEverywhere(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()

	id, source := resolveInstallID(context.Background(), store)

	require.Equal(t, SourceGenerated, source)
	_, err := uuid.Parse(id)
	require.NoError(t, err, "id %q is not a UUID: %v", id, err)
	got := store.value(InstallIDKey)
	assert.Equal(t, id, got)
	got = readFile(t, path)
	assert.Equal(t, id+"\n", got)

	again, source := resolveInstallID(context.Background(), store)
	assert.Equal(t, id, again)
	assert.Equal(t, SourceDatabase, source)
}

func TestResolveInstallIDMigratesExistingFileToDatabaseUnchanged(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, existingID)
	store := newFakeStore()

	id, source := resolveInstallID(context.Background(), store)

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

	id, source := resolveInstallID(context.Background(), store)

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

	id, source := resolveInstallID(context.Background(), store)

	require.Equal(t, existingID, id)
	require.Equal(t, SourceFile, source)
	assert.Equal(t, 0, store.sets)
}

func TestResolveInstallIDKeepsFileWhenDatabaseWriteFails(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()
	store.fail(nil, errors.New("read-only transaction"))

	id, source := resolveInstallID(context.Background(), store)
	require.Equal(t, SourceGenerated, source)
	got := readFile(t, path)
	require.Equal(t, id+"\n", got, "file holds %q after a failed database write, want %q", got, id)

	// The file is what survives; a later start without the database reads it.
	again, source := resolveInstallID(context.Background(), nil)
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

	id, source := resolveInstallID(context.Background(), store)

	require.NotEmpty(t, id)
	require.NotEqual(t, " ", id)
	require.Equal(t, SourceGenerated, source)
}

func TestIdentityRecoversDatabaseIDAfterOutage(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	store.values[InstallIDKey] = existingID
	store.fail(errors.New("connection refused"), errors.New("connection refused"))
	identity := NewIdentity(store)

	// No file, database down: a provisional id is the best available, and it
	// must stay the same provisional id while the outage lasts.
	provisional, source := identity.Resolve(context.Background())
	require.Equal(t, SourceGenerated, source)
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
			ids[r] = NewIdentity(store).ID(context.Background())
		})
	}
	wg.Wait()

	winner := store.value(InstallIDKey)
	for r, id := range ids {
		assert.Equal(t, winner, id, "replica %d kept %q, database holds %q", r, id, winner)
	}
}

// The master key must not reach the identifier: it is sent on update checks,
// and an id derived from the key would let a weak key be guessed offline.
func TestResolveInstallIDIsNotDerivedFromMasterKey(t *testing.T) {
	useTempDataDir(t)
	first, source := resolveInstallID(context.Background(), nil)
	require.Equal(t, SourceGenerated, source)
	_, err := uuid.Parse(first)
	require.NoError(t, err, "generated id %q is not a UUID: %v", first, err)

	// A recreated container without a volume or database gets a new id.
	useTempDataDir(t)
	second, source := resolveInstallID(context.Background(), nil)
	require.Equal(t, SourceGenerated, source)
	assert.NotEqual(t, first, second)
}

func TestResolveInstallIDWithoutStoreUsesFile(t *testing.T) {
	path := useTempDataDir(t)

	id, source := resolveInstallID(context.Background(), nil)
	require.Equal(t, SourceGenerated, source)
	got := readFile(t, path)
	require.Equal(t, id+"\n", got, "file holds %q, want %q", got, id)

	again, source := resolveInstallID(context.Background(), nil)
	assert.Equal(t, id, again)
	assert.Equal(t, SourceFile, source)
}

// An upgraded deployment may still hold an id derived from the master key, in
// the database, the file, or both. It must be replaced everywhere rather than
// sent again.
func TestResolveInstallIDReplacesDerivedID(t *testing.T) {
	tests := []struct {
		name    string
		inStore bool
		inFile  bool
	}{
		{name: "database and file", inStore: true, inFile: true},
		{name: "database only", inStore: true},
		{name: "file only", inFile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := useTempDataDir(t)
			store := newFakeStore()
			if tt.inStore {
				store.values[InstallIDKey] = derivedID
			}
			if tt.inFile {
				writeFile(t, path, derivedID)
			}
			identity := NewIdentity(store)

			id, source := identity.Resolve(context.Background())
			require.Equal(t, SourceGenerated, source)
			require.NotEqual(t, derivedID, id)
			require.False(t, derivedFromSecret(id), "replacement %q is still version 8", id)
			assert.Equal(t, id, store.value(InstallIDKey))
			assert.Equal(t, id+"\n", readFile(t, path))

			// The next check confirms the replacement from the database.
			again, _ := identity.Resolve(context.Background())
			assert.Equal(t, id, again)
		})
	}
}

// Replicas that upgrade together each try to replace the derived id; all of
// them end up reporting the one replacement the database kept.
func TestIdentityConvergesWhenReplicasReplaceDerivedID(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	store.values[InstallIDKey] = derivedID

	const replicas = 8
	ids := make([]string, replicas)
	var wg sync.WaitGroup
	for r := range replicas {
		wg.Go(func() { ids[r] = NewIdentity(store).ID(context.Background()) })
	}
	wg.Wait()

	winner := store.value(InstallIDKey)
	require.False(t, derivedFromSecret(winner))
	for r, id := range ids {
		assert.Equal(t, winner, id, "replica %d", r)
	}
}

// staleReadStore answers Get with the value it saw before another replica
// wrote, as a replica does when it reads just before that write.
type staleReadStore struct {
	*fakeStore
	stale string
}

func (s staleReadStore) Get(context.Context, string) (string, bool, error) {
	return s.stale, true, nil
}

// A replica that read the derived id just before another replica replaced it
// must adopt that replacement instead of overwriting it.
func TestIdentityAdoptsReplacementWrittenAfterItsRead(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	store.values[InstallIDKey] = derivedID

	first, _ := NewIdentity(store).Resolve(context.Background())
	require.Equal(t, first, store.value(InstallIDKey))

	useTempDataDir(t) // replicas share the database, not the data directory
	second, source := NewIdentity(staleReadStore{fakeStore: store, stale: derivedID}).Resolve(context.Background())
	assert.Equal(t, first, second)
	assert.Equal(t, SourceDatabase, source)
	assert.Equal(t, first, store.value(InstallIDKey))
}

func TestDerivedFromSecret(t *testing.T) {
	assert.True(t, derivedFromSecret(derivedID))
	assert.True(t, derivedFromSecret(" "+derivedID+"\n"))
	assert.False(t, derivedFromSecret(existingID))
	assert.False(t, derivedFromSecret(uuid.NewString()))
	assert.False(t, derivedFromSecret(""))
	assert.False(t, derivedFromSecret("not-a-uuid"))
}
