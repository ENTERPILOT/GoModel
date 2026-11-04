package run

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A stopping generation stops accepting by setting a past deadline on its
// listener (see server.QuiesceTimeout); a listener without SetDeadline would
// keep accepting connections it then drops. This pins that every listener
// boundSocket hands out supports it, and that the deadline belongs to that
// generation alone: a connection arriving meanwhile waits for the next
// listener on the same socket, which still accepts it.
func TestBoundSocketListenersStopAcceptingOnDeadline(t *testing.T) {
	socket := testSocket(t)

	first, err := socket.next()
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	deadliner, ok := first.(interface{ SetDeadline(time.Time) error })
	require.True(t, ok, "listener %T has no SetDeadline", first)
	require.NoError(t, deadliner.SetDeadline(time.Unix(1, 0)))
	_, err = first.Accept()
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout(), "Accept after a past deadline = %v, want a timeout", err)

	client, err := net.Dial("tcp", first.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	second, err := socket.next()
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	conn, err := second.Accept()
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}
