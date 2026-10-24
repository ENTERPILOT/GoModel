package run

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/require"
)

// An extension notifies the Secrets of the generation it saw last. When that
// generation's reload was rejected, the notification must still reach the
// generation that is serving, which re-checks with its own resolvers.
func TestRunRoutesSecretNotificationsToTheServingGeneration(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server:\n  master_key: ${vault:master}\n"), 0o600))
	t.Setenv("GOMODEL_MASTER_KEY", "")
	t.Setenv("PORT", "0")
	t.Setenv("GOMODEL_OFFLINE", "true")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(dir, "gomodel.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")

	var mu sync.Mutex
	master := "m1"
	resolved := make(chan struct{})
	var resolvedOnce sync.Once
	vault := config.SecretResolverFunc(func(context.Context, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		resolvedOnce.Do(func() { close(resolved) })
		return master, nil
	})

	setup := make(chan *config.Secrets, 1)
	reloads := make(chan *config.Secrets, 4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- Run(ctx, Options{
			Args:   []string{},
			Stdout: io.Discard,
			Stderr: io.Discard,
			SetupConfig: func(_ context.Context, result *config.LoadResult) error {
				setup <- result.Secrets
				return result.Secrets.Register("vault", vault)
			},
			ReloadConfig: func(_ context.Context, result *config.LoadResult) error {
				if err := result.Secrets.Register("vault", vault); err != nil {
					return err
				}
				reloads <- result.Secrets
				return errors.New("rejected by the test")
			},
		})
	}()

	waitFor := func(ch <-chan *config.Secrets, what string) *config.Secrets {
		t.Helper()
		select {
		case secrets := <-ch:
			return secrets
		case err := <-served:
			t.Fatalf("Run returned before %s: %v", what, err)
		case <-time.After(30 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
		return nil
	}

	first := waitFor(setup, "the first generation")
	select {
	case <-resolved:
	case <-time.After(30 * time.Second):
		t.Fatal("the first generation never resolved its master key")
	}
	mu.Lock()
	master = "m2"
	mu.Unlock()

	// The serving generation sees server.master_key changed and reloads.
	first.NotifyChanged()
	abandoned := waitFor(reloads, "the reload a rotated master key requests")
	require.NotSame(t, first, abandoned)

	// That reload was rejected. Notifying the abandoned generation's Secrets
	// still reaches the serving one, which still sees the change pending.
	abandoned.NotifyChanged()
	waitFor(reloads, "the reload requested through the abandoned generation")

	cancel()
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}
