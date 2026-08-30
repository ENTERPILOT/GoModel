package versioncheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

type fakeStore struct {
	values map[string]string
	getErr error
	setErr error
	sets   int
}

func newFakeStore() *fakeStore { return &fakeStore{values: map[string]string{}} }

func (s *fakeStore) Get(_ context.Context, key string) (string, bool, error) {
	if s.getErr != nil {
		return "", false, s.getErr
	}
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *fakeStore) Set(_ context.Context, key, value string) error {
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func TestResolveInstallIDGeneratesAndPersistsEverywhere(t *testing.T) {
	path := useTempDataDir(t)
	store := newFakeStore()

	id, source := ResolveInstallID(context.Background(), store, "")

	if source != SourceGenerated {
		t.Fatalf("source = %q, want %q", source, SourceGenerated)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("id %q is not a UUID: %v", id, err)
	}
	if got := store.values[InstallIDKey]; got != id {
		t.Errorf("database holds %q, want %q", got, id)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Errorf("file holds %q, want %q", got, id+"\n")
	}

	again, source := ResolveInstallID(context.Background(), store, "")
	if again != id || source != SourceDatabase {
		t.Errorf("second resolve = %q (%s), want %q (%s)", again, source, id, SourceDatabase)
	}
}

func TestResolveInstallIDMigratesExistingFileToDatabaseUnchanged(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, "3f2a0000-0000-4000-8000-000000000001")
	store := newFakeStore()

	id, source := ResolveInstallID(context.Background(), store, "secret")

	if id != "3f2a0000-0000-4000-8000-000000000001" || source != SourceFile {
		t.Fatalf("resolve = %q (%s), want the file's id from %s", id, source, SourceFile)
	}
	if got := store.values[InstallIDKey]; got != id {
		t.Errorf("database holds %q after migration, want %q", got, id)
	}
}

func TestResolveInstallIDDatabaseWinsOverRegeneratedFile(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, "ffffffff-0000-4000-8000-00000000fresh")
	store := newFakeStore()
	store.values[InstallIDKey] = "3f2a0000-0000-4000-8000-000000000001"

	id, source := ResolveInstallID(context.Background(), store, "")

	if id != "3f2a0000-0000-4000-8000-000000000001" || source != SourceDatabase {
		t.Fatalf("resolve = %q (%s), want the database's id", id, source)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Errorf("file not restored from database: %q", got)
	}
}

func TestResolveInstallIDKeepsFileWhenDatabaseErrors(t *testing.T) {
	path := useTempDataDir(t)
	writeFile(t, path, "3f2a0000-0000-4000-8000-000000000001")
	store := newFakeStore()
	store.getErr = errors.New("connection refused")

	id, source := ResolveInstallID(context.Background(), store, "secret")

	if id != "3f2a0000-0000-4000-8000-000000000001" || source != SourceFile {
		t.Fatalf("resolve = %q (%s), want the file's id; a failing store must never mint a new one", id, source)
	}
	if store.sets != 0 {
		t.Errorf("wrote to a failing store %d times", store.sets)
	}
}

func TestResolveInstallIDDerivesFromSecretWhenNothingStored(t *testing.T) {
	useTempDataDir(t)

	first, source := ResolveInstallID(context.Background(), nil, "operator-secret")
	if source != SourceDerived {
		t.Fatalf("source = %q, want %q", source, SourceDerived)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("derived id %q is not a UUID: %v", first, err)
	}

	// A recreated container: no file, same secret, same id.
	useTempDataDir(t)
	second, _ := ResolveInstallID(context.Background(), nil, "operator-secret")
	if second != first {
		t.Errorf("same secret derived %q then %q", first, second)
	}

	useTempDataDir(t)
	other, _ := ResolveInstallID(context.Background(), nil, "another-secret")
	if other == first {
		t.Error("different secrets derived the same id")
	}
}

func TestResolveInstallIDWithoutStoreUsesFile(t *testing.T) {
	path := useTempDataDir(t)

	id, source := ResolveInstallID(context.Background(), nil, "")
	if source != SourceGenerated {
		t.Fatalf("source = %q, want %q", source, SourceGenerated)
	}
	if got := readFile(t, path); got != id+"\n" {
		t.Fatalf("file holds %q, want %q", got, id)
	}

	again, source := ResolveInstallID(context.Background(), nil, "")
	if again != id || source != SourceFile {
		t.Errorf("second resolve = %q (%s), want %q (%s)", again, source, id, SourceFile)
	}
}
