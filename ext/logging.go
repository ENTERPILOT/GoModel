package ext

import "log/slog"

// WrapLogHandler adds a wrapper around every slog handler core builds: the
// default logger installed at startup, the one each configuration reload
// installs before it loads the configuration, and any other logger core
// creates. Derived loggers (With, WithGroup) and the log package, which
// slog.SetDefault routes through the default handler, keep the wrapper. It is
// meant for output filters such as redacting secret values; it wraps the
// handler, not the writer, so core's terminal detection and level stay intact.
//
// Wrappers apply in registration order: the first wraps core's handler and
// each later one wraps the result, so the last registered sees a record first.
// A wrapper must return a non-nil handler that is safe for concurrent use;
// returning nil fails startup or rejects the reload.
//
// Register before calling run.Run. Core reads the wrappers once when Run
// starts, before it builds its first logger, and reuses them for every
// generation, so a wrapper registered later (including from Options.Setup)
// is never applied. A nil wrapper is ignored.
func (r *Registry) WrapLogHandler(wrap func(slog.Handler) slog.Handler) {
	if wrap == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logHandlerWrappers = append(r.logHandlerWrappers, wrap)
}

// LogHandlerWrappers returns a defensive copy of the registered log handler
// wrappers, in registration order.
func (r *Registry) LogHandlerWrappers() []func(slog.Handler) slog.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]func(slog.Handler) slog.Handler(nil), r.logHandlerWrappers...)
}

// WrapLogHandler registers a log handler wrapper on the Default registry.
func WrapLogHandler(wrap func(slog.Handler) slog.Handler) { Default.WrapLogHandler(wrap) }
