//go:build linux || darwin || freebsd

package pluginload

import (
	"errors"
	"plugin"
)

// Supported reports whether this platform can open shared objects. Go's
// plugin package works on Linux, macOS, and FreeBSD only, and only in
// cgo-enabled binaries; the latter is detected at open time.
const Supported = true

func openSymbols(path string) (symbols, error) {
	p, err := plugin.Open(path)
	if err != nil {
		return symbols{}, err
	}
	sym, err := p.Lookup(PluginSymbol)
	if err != nil {
		return symbols{}, errMissingSymbol(err)
	}
	out := symbols{plugin: sym}
	if bi, err := p.Lookup(BuildInfoSymbol); err == nil {
		out.buildInfo = bi
	}
	return out, nil
}

// errMissingSymbol marks a lookup failure so describeOpenError can explain
// what the plugin must export.
type missingSymbolError struct{ err error }

func (e *missingSymbolError) Error() string { return e.err.Error() }
func (e *missingSymbolError) Unwrap() error { return e.err }

func errMissingSymbol(err error) error { return &missingSymbolError{err: err} }

func isMissingSymbol(err error) bool {
	var m *missingSymbolError
	return errors.As(err, &m)
}
