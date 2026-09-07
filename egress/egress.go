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
	"errors"
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
// dial. It belongs to whatever composes the process - a distribution's
// startup path - and there is one: installing over an existing hook is an
// error rather than a silent replacement, so a policy cannot be swapped out
// from under the clients enforcing it. Uninstall removes it again.
//
// Connections already open are unaffected: a hook installed at startup, as
// GoModel Pro's air-gapped mode does before the application is built, sees
// every connection the gateway makes.
func Install(dial DialFunc) error {
	if dial == nil {
		return errors.New("egress: dial hook is required; call Uninstall to remove one")
	}
	if !installed.CompareAndSwap(nil, &dial) {
		return errors.New("egress: a dial hook is already installed")
	}
	return nil
}

// Uninstall removes the installed hook, so core's clients dial directly
// again. Whoever installed the hook calls it: GoModel Pro's guard does on
// close.
func Uninstall() { installed.Store(nil) }

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

// Lookup resolves host for a client that resolves names itself before it
// dials. With a hook installed the name is passed through untouched, so the
// hook decides what it may resolve to and dials the address that passed;
// without one the client's own resolver answers, keeping its behaviour -
// multiple addresses and their fallbacks included - exactly as it was.
//
// It is consulted per connection, never snapshotted at client construction:
// a client built before the hook was installed would otherwise keep handing
// it addresses it had already chosen, which no hostname policy can judge.
func Lookup(ctx context.Context, host string, resolve func(context.Context, string) ([]string, error)) ([]string, error) {
	if Installed() {
		return []string{host}, nil
	}
	return resolve(ctx, host)
}

// Dialer adapts DialContext to the interface the MongoDB driver takes.
type Dialer struct{}

// DialContext implements the driver's dialer interface.
func (Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return DialContext(ctx, network, address)
}
