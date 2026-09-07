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
	t.Cleanup(func() { Install(nil) })

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
	Install(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, refused
	})
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

	// Installing nil restores the default dialer.
	Install(nil)
	if Installed() {
		t.Fatal("Install(nil) must remove the hook")
	}
	conn, err = DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("default dialer after removal: %v", err)
	}
	conn.Close()
}
