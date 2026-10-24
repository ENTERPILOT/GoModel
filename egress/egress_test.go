package egress

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialContextUsesTheInstalledHook(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
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
	require.NoError(t, err, "default dialer")
	conn.Close()
	require.False(t, Installed(), "no hook is installed yet")

	// With one, every connection goes through it - including the ones opened
	// through the Dialer adapter.
	refused := errors.New("outside the air gap")
	var dialed []string
	hook, err := Install(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, refused
	})
	require.NoError(t, err)
	held = append(held, hook)
	require.True(t, Installed(), "Installed must report the hook")
	_, err = DialContext(context.Background(), "tcp", "db.example.com:5432")
	require.ErrorIs(t, err, refused, "hook error must be the hook's refusal")
	_, err = (Dialer{}).DialContext(context.Background(), "tcp", "cache.example.com:6379")
	require.ErrorIs(t, err, refused, "adapter error must be the hook's refusal")
	require.Equal(t, []string{"db.example.com:5432", "cache.example.com:6379"}, dialed)

	// A second hook is refused rather than silently replacing the first: the
	// policy belongs to whoever composed the process.
	_, err = Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	require.Error(t, err, "installing over an existing hook must be an error")
	_, err = DialContext(context.Background(), "tcp", "db.example.com:5432")
	require.ErrorIs(t, err, refused, "the first hook must still be in force")
	_, err = Install(nil)
	require.Error(t, err, "a nil dial must be an error; the handle removes a hook")

	// The handle its owner holds restores the default dialer, and says so
	// only once: a stale handle must not remove a policy someone else
	// installed afterwards.
	hook.Uninstall()
	require.False(t, Installed(), "the handle must remove the hook")
	replacement, err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, refused })
	require.NoError(t, err)
	held = append(held, replacement)
	hook.Uninstall()
	require.True(t, Installed(), "a stale handle must not remove the current hook")
	replacement.Uninstall()
	conn, err = DialContext(context.Background(), "tcp", listener.Addr().String())
	require.NoError(t, err, "default dialer after removal")
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
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.4"}, got, "without a hook the client's own resolver answers")

	// The hook arrives afterwards; the same client must now hand it the name.
	hook, err := Install(func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	require.NoError(t, err)
	t.Cleanup(hook.Uninstall)
	got, err = Lookup(context.Background(), "db.internal", resolve)
	require.NoError(t, err)
	assert.Equal(t, []string{"db.internal"}, got, "a hook installed later must still see the hostname")
}
