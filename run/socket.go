package run

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

// boundSocket owns the gateway's listening socket for the lifetime of the
// process so that a configuration reload never gives the port up.
//
// Each generation of the application is handed its own listener and closes it
// when it stops; the socket itself survives because boundSocket holds a
// duplicate descriptor and is the last to let go. Connections that arrive
// while one generation is draining and the next is starting therefore wait in
// the kernel's accept queue instead of being refused.
//
// Duplicating the descriptor is what keeps the socket alive, and not every
// platform and listener type supports it. Where it fails, the listener is
// served directly and a later generation rebinds the address instead, which
// does leave a gap where connections are refused. Windows never reaches that
// gap for a different reason: Go delivers no SIGHUP there, so nothing asks for
// a second generation in the first place.
type boundSocket struct {
	address  string
	file     *os.File
	listener net.Listener
}

// listenOn binds address and keeps hold of it for the process's lifetime.
func listenOn(address string) (*boundSocket, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		return &boundSocket{address: address, listener: listener}, nil
	}
	file, err := tcp.File()
	if err != nil {
		slog.Debug("listening socket cannot be duplicated; a reload will rebind the address",
			"address", address, "error", err)
		return &boundSocket{address: address, listener: listener}, nil
	}
	// The socket stays open through the duplicate held in file.
	_ = listener.Close()
	return &boundSocket{address: address, file: file}, nil
}

// next returns the listener for the next application generation.
func (s *boundSocket) next() (net.Listener, error) {
	if s.file != nil {
		listener, err := net.FileListener(s.file)
		if err != nil {
			return nil, fmt.Errorf("reuse listening socket on %s: %w", s.address, err)
		}
		return listener, nil
	}
	if s.listener != nil {
		listener := s.listener
		s.listener = nil
		return listener, nil
	}
	return net.Listen("tcp", s.address)
}

// Close releases the socket for good, which is the process exiting rather than
// a generation ending.
func (s *boundSocket) Close() error {
	if s.listener != nil {
		defer func() { s.listener = nil }()
		return s.listener.Close()
	}
	if s.file != nil {
		defer func() { s.file = nil }()
		return s.file.Close()
	}
	return nil
}
