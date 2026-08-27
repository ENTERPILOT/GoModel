package versioncheck

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

// installIDFile stores the anonymous per-deployment identifier next to the
// gateway's other durable state.
const installIDFile = "install-id"

// installID memoizes the identifier so a data directory that cannot be written
// still yields one stable value for the process instead of a fresh id per call.
var installID = sync.OnceValue(readOrCreateInstallID)

// InstallID returns a stable, anonymous identifier for this deployment,
// creating one on first use. It is a random UUID: it encodes nothing about
// the host, the operator, or the configuration, and only ever leaves the
// process on an update check.
//
// A read-only or unwritable data directory is not an error; the caller gets a
// process-lifetime identifier instead.
func InstallID() string { return installID() }

func readOrCreateInstallID() string {
	path := platformdir.DataFile(installIDFile)
	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			return id
		}
	}
	id := uuid.NewString()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		// 0600: the id is not a secret, but it is per-deployment state and
		// has no reason to be world-readable.
		_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
	}
	return id
}
