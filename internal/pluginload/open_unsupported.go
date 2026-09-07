//go:build !(linux || darwin || freebsd)

package pluginload

// Supported reports whether this platform can open shared objects. Go's
// plugin package does not support this platform.
const Supported = false

func openSymbols(string) (symbols, error) {
	return symbols{}, errUnsupported()
}

func isMissingSymbol(error) bool { return false }
