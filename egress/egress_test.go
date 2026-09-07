package egress

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestDialContextUsesTheInstalledHook(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Cleanup goes through the handles the test holds, the way any other
	// owner removes a hook - never by reaching into the package's state.
	var held []*Hook
	t.Cleanup(func() {
		for _, hook := range held {
			hook.Uninstall()
		}
	})

	// Without a hook the default dialer connects.
	conn, err := DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("default dialer: %v", err)
	}
	conn.Close()
	if Installed() {
		t.Fatal("no hook is installed yet")
	}

	// With one, every connection goes through it - including the ones opened
	// through the Dialer adapter.
	refused := errors.New("outside the air gap")
	var dialed []string
	hook, err := Install(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, refused
	})
	if err != nil {
		t.Fatal(err)
	}
	held = append(held, hook)
	if !Installed() {
		t.Fatal("Installed must report the hook")
	}
	if _, err := DialContext(context.Background(), "tcp", "db.example.com:5432"); !errors.Is(err, refused) {
		t.Fatalf("hook error = %v, want the hook's refusal", err)
	}
	if _, err := (Dialer{}).DialContext(context.Background(), "tcp", "cache.example.com:6379"); !errors.Is(err, refused) {
		t.Fatalf("adapter error = %v, want the hook's refusal", err)
	}
	if len(dialed) != 2 || dialed[0] != "db.example.com:5432" || dialed[1] != "cache.example.com:6379" {
		t.Fatalf("hook saw %v", dialed)
	}

	// A second hook is refused rather than silently replacing the first: the
	// policy belongs to whoever composed the process.
	if _, err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err == nil {
		t.Fatal("installing over an existing hook must be an error")
	}
	if _, err := DialContext(context.Background(), "tcp", "db.example.com:5432"); !errors.Is(err, refused) {
		t.Fatalf("the first hook must still be in force, got %v", err)
	}
	if _, err := Install(nil); err == nil {
		t.Fatal("a nil dial must be an error; the handle removes a hook")
	}

	// The handle its owner holds restores the default dialer, and says so
	// only once: a stale handle must not remove a policy someone else
	// installed afterwards.
	hook.Uninstall()
	if Installed() {
		t.Fatal("the handle must remove the hook")
	}
	replacement, err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, refused })
	if err != nil {
		t.Fatal(err)
	}
	held = append(held, replacement)
	hook.Uninstall()
	if !Installed() {
		t.Fatal("a stale handle must not remove the current hook")
	}
	replacement.Uninstall()
	conn, err = DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("default dialer after removal: %v", err)
	}
	conn.Close()
}

// Clients that resolve a name themselves before dialing ask Lookup which
// answer to use. The decision is made per connection: a client built before
// a hook was installed would otherwise keep handing it addresses it had
// already chosen, which no hostname policy can judge.
func TestLookupFollowsTheHookAtCallTime(t *testing.T) {
	resolved := []string{"10.0.0.4"}
	resolve := func(context.Context, string) ([]string, error) { return resolved, nil }

	// A client constructed with no hook installed.
	got, err := Lookup(context.Background(), "db.internal", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "10.0.0.4" {
		t.Fatalf("without a hook the client's own resolver answers, got %v", got)
	}

	// The hook arrives afterwards; the same client must now hand it the name.
	hook, err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hook.Uninstall)
	got, err = Lookup(context.Background(), "db.internal", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "db.internal" {
		t.Fatalf("a hook installed later must still see the hostname, got %v", got)
	}
}
