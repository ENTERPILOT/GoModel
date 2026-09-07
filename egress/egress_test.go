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
	t.Cleanup(Uninstall)

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
	if err := Install(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, refused
	}); err != nil {
		t.Fatal(err)
	}
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
	if err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err == nil {
		t.Fatal("installing over an existing hook must be an error")
	}
	if _, err := DialContext(context.Background(), "tcp", "db.example.com:5432"); !errors.Is(err, refused) {
		t.Fatalf("the first hook must still be in force, got %v", err)
	}
	if err := Install(nil); err == nil {
		t.Fatal("Install(nil) must be an error; Uninstall removes the hook")
	}

	// Uninstall restores the default dialer.
	Uninstall()
	if Installed() {
		t.Fatal("Uninstall must remove the hook")
	}
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
	t.Cleanup(Uninstall)
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
	if err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	got, err = Lookup(context.Background(), "db.internal", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "db.internal" {
		t.Fatalf("a hook installed later must still see the hostname, got %v", got)
	}
}
