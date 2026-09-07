// Package egress carries the dial hook a distribution installs to police the
// connections GoModel opens directly.
//
// HTTP clients are steered by the standard proxy variables, so a
// distribution that wants to see every outbound HTTP request only has to set
// HTTP_PROXY. The database, cache, and vector store clients speak their own
// protocols over raw TCP and read no such variable: without a hook they
// connect wherever their URL points, whatever policy the distribution
// believes it is enforcing. This package is that hook.
//
// It is process-wide on purpose, matching the proxy variables it
// complements: the clients live behind several layers of construction, and a
// guarantee an operator is told covers the process cannot depend on a
// parameter each of them remembers to pass along.
package egress

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// DialFunc opens one connection. It matches net.Dialer.DialContext, which is
// what the database and cache clients expect.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

var installed atomic.Pointer[DialFunc]

// defaultDial is what core uses until a distribution installs a hook.
var defaultDial DialFunc = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext

// Install routes every direct connection core opens from now on through
// dial. A nil dial restores the default dialer. Installing replaces any
// previous hook, so a distribution keeps one policy rather than a chain.
//
// Connections already open are unaffected: a hook installed at startup, as
// GoModel Pro's air-gapped mode does before the application is built, sees
// every connection the gateway makes.
func Install(dial DialFunc) {
	if dial == nil {
		installed.Store(nil)
		return
	}
	installed.Store(&dial)
}

// Installed reports whether a distribution has installed a hook.
func Installed() bool { return installed.Load() != nil }

// DialContext opens a connection through the installed hook, or with the
// default dialer when there is none. Clients that take a dialer are given
// this function; clients that take an interface are given Dialer.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if dial := installed.Load(); dial != nil {
		return (*dial)(ctx, network, address)
	}
	return defaultDial(ctx, network, address)
}

// Dialer adapts DialContext to the interface the MongoDB driver takes.
type Dialer struct{}

// DialContext implements the driver's dialer interface.
func (Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return DialContext(ctx, network, address)
}
