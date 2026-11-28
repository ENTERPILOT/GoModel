package config

import (
	"errors"
	"log/slog"
	"strings"
)

// minMasterKeyLength is the length below which a master key is reported as
// weak. A shorter key still works, so existing deployments keep starting.
const minMasterKeyLength = 16

// masterKeyPlaceholders are the fill-in master keys GoModel's templates and
// configuration documentation show. They are public, so a gateway started with
// one is as open as a gateway without a key. The runnable local examples in
// the guides use change-me; it is short, so it draws the weak-key warning
// instead of stopping those copy-paste commands.
var masterKeyPlaceholders = map[string]struct{}{
	"my-secret-key":        {},
	"your-secret-key":      {},
	"your-secret-key-here": {},
}

// rejectPlaceholderMasterKey fails on a master key copied from the templates
// or the documentation. An empty key is left to the startup warning about
// running without authentication.
func rejectPlaceholderMasterKey(key string) error {
	if _, placeholder := masterKeyPlaceholders[strings.ToLower(strings.TrimSpace(key))]; placeholder {
		return errors.New("server.master_key (GOMODEL_MASTER_KEY) is a placeholder from the GoModel templates or documentation; replace it with a key of your own, for example from `openssl rand -hex 32`")
	}
	return nil
}

// warnWeakMasterKey reports a master key short enough to guess. It only
// warns: the key keeps working.
func warnWeakMasterKey(key string) {
	if key == "" || len(key) >= minMasterKeyLength {
		return
	}
	slog.Warn("SECURITY WARNING: GOMODEL_MASTER_KEY is short and may be guessable",
		"recommended_min_length", minMasterKeyLength,
		"recommendation", "generate a key with `openssl rand -hex 32`")
}
