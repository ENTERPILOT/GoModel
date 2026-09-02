package versioncheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// useTempDataDir points platformdir's project-local data directory at a fresh
// temp dir, so each test starts without an install-id file.
func useTempDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return filepath.Join(dir, "data", installIDFile)
}

func writeFile(t *testing.T, path, id string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

	if source != SourceGenerated {
		t.Fatalf("source = %q, want %q", source, SourceGenerated)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("id %q is not a UUID: %v", id, err)
	}
	if got := store.value(InstallIDKey); got != id {
		t.Errorf("database holds %q, want %q", got, id)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Errorf("file holds %q, want %q", got, id+"\n")
	}

	again, source := resolveInstallID(context.Background(), store, "")
	if again != id || source != SourceDatabase {
		t.Errorf("second resolve = %q (%s), want %q (%s)", again, source, id, SourceDatabase)
	}
}

func TestResolveInstallIDMigratesExistingFileToDatabaseUnchanged(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, existingID)
	store := newFakeStore()

	id, source := resolveInstallID(context.Background(), store, "secret")

	if id != existingID || source != SourceFile {
		t.Fatalf("resolve = %q (%s), want the file's id from %s", id, source, SourceFile)
	}
	if got := store.value(InstallIDKey); got != id {
		t.Errorf("database holds %q after migration, want %q", got, id)
	}
}

func TestResolveInstallIDDatabaseWinsOverRegeneratedFile(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, freshID)
	store := newFakeStore()
	store.values[InstallIDKey] = existingID

	id, source := resolveInstallID(context.Background(), store, "")

	if id != existingID || source != SourceDatabase {
		t.Fatalf("resolve = %q (%s), want the database's id", id, source)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Errorf("file not restored from database: %q", got)
	}
}

func TestResolveInstallIDKeepsFileWhenDatabaseErrors(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, existingID)
	store := newFakeStore()
	store.fail(errors.New("connection refused"), nil)

	id, source := resolveInstallID(context.Background(), store, "secret")

	if id != existingID || source != SourceFile {
		t.Fatalf("resolve = %q (%s), want the file's id; a failing store must never mint a new one", id, source)
	}
	if store.sets != 0 {
		t.Errorf("wrote to a failing store %d times", store.sets)
	}
}

func TestResolveInstallIDKeepsFileWhenDatabaseWriteFails(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()
	store.fail(nil, errors.New("read-only transaction"))

	id, source := resolveInstallID(context.Background(), store, "")
	if source != SourceGenerated {
		t.Fatalf("source = %q, want %q", source, SourceGenerated)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Fatalf("file holds %q after a failed database write, want %q", got, id)
	}

	// The file is what survives; a later start without the database reads it.
	again, source := resolveInstallID(context.Background(), nil, "")
	if again != id || source != SourceFile {
		t.Errorf("later resolve = %q (%s), want %q (%s)", again, source, id, SourceFile)
	}
}

func TestResolveInstallIDNeverAdoptsBlankDatabaseValue(t *testing.T) {
	useTempDataDir(t)
	store := newFakeStore()
	// Pathological: something left a blank value under the key. SetDefault
	// keeps it (insert-if-absent), so its read-back returns the blank; the
	// resolved id must be the local candidate, never the blank.
	store.values[InstallIDKey] = " "

	id, source := resolveInstallID(context.Background(), store, "secret")

	if id == "" || id == " " || source != SourceDerived {
		t.Fatalf("resolve = %q (%s), want the derived candidate", id, source)
	}
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
	if source != SourceDerived || provisional == existingID {
		t.Fatalf("during outage: %q (%s), want a derived provisional id", provisional, source)
	}
	if again := identity.ID(context.Background()); again != provisional {
		t.Fatalf("provisional id changed during outage: %q then %q", provisional, again)
	}

	store.fail(nil, nil)
	id, source := identity.Resolve(context.Background())
	if id != existingID || source != SourceDatabase {
		t.Fatalf("after outage: %q (%s), want the database's id", id, source)
	}
	if got := store.value(InstallIDKey); got != existingID {
		t.Errorf("database overwritten with the provisional id: %q", got)
	}
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
		if id != winner {
			t.Errorf("replica %d kept %q, database holds %q", r, id, winner)
		}
	}
}

func TestResolveInstallIDDerivesFromSecretWhenNothingStored(t *testing.T) {
	useTempDataDir(t)

	first, source := resolveInstallID(context.Background(), nil, "operator-secret")
	if source != SourceDerived {
		t.Fatalf("source = %q, want %q", source, SourceDerived)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("derived id %q is not a UUID: %v", first, err)
	}

	// A recreated container: no file, same secret, same id.
	useTempDataDir(t)
	second, _ := resolveInstallID(context.Background(), nil, "operator-secret")
	if second != first {
		t.Errorf("same secret derived %q then %q", first, second)
	}

	useTempDataDir(t)
	other, _ := resolveInstallID(context.Background(), nil, "another-secret")
	if other == first {
		t.Error("different secrets derived the same id")
	}
}

func TestResolveInstallIDWithoutStoreUsesFile(t *testing.T) {
	path := useTempDataDir(t)

	id, source := resolveInstallID(context.Background(), nil, "")
	if source != SourceGenerated {
		t.Fatalf("source = %q, want %q", source, SourceGenerated)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Fatalf("file holds %q, want %q", got, id)
	}

	again, source := resolveInstallID(context.Background(), nil, "")
	if again != id || source != SourceFile {
		t.Errorf("second resolve = %q (%s), want %q (%s)", again, source, id, SourceFile)
	}
}
