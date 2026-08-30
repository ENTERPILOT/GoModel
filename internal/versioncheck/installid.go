package versioncheck

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/platformdir"
)

// installIDFile stores the anonymous per-deployment identifier next to the
// gateway's other durable state.
const installIDFile = "install-id"

// InstallIDKey is the key the identifier is kept under in the deployment's
// database, alongside the runtime settings.
const InstallIDKey = "install_id"

// derivedIDPurpose is the fixed message signed with the operator's secret
// when the identifier has to be derived. It ties the derivation to this use,
// so the same secret used for anything else yields an unrelated value.
const derivedIDPurpose = "gomodel install identifier v1"

// Store is the durable key/value the identifier is kept in. It matches
// runtimesettings.Store: the identifier is per-deployment state exactly like
// a runtime setting, so it shares the table.
type Store interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key, value string) error
}

// InstallIDSource names where a resolved identifier came from, for the
// startup log.
type InstallIDSource string

const (
	// SourceDatabase: read from the deployment's database.
	SourceDatabase InstallIDSource = "database"
	// SourceFile: read from the install-id file in the data directory.
	SourceFile InstallIDSource = "file"
	// SourceDerived: computed from the operator's secret because nothing was
	// stored; stable as long as the secret is.
	SourceDerived InstallIDSource = "derived"
	// SourceGenerated: freshly minted; nothing durable was available.
	SourceGenerated InstallIDSource = "generated"
)

// ResolveInstallID returns the stable, anonymous identifier for this
// deployment, creating one on first use. It is a random UUID that encodes
// nothing about the host, the operator, or the configuration, and only ever
// leaves the process on an update check.
//
// "This deployment" is defined by whatever survives longest, in this order:
//
//  1. The database, when a store is given. A gateway's database outlives its
//     container (it is on a volume or on another host), so the identifier
//     lives there first, and replicas sharing one database count as one.
//  2. The install-id file in the data directory. The canonical location
//     before the database was used; an existing id migrates to the database
//     unchanged, so upgrading never creates a new deployment.
//  3. An HMAC of the operator's secret, when one is configured. Nothing on
//     disk survived (a container recreated without a volume) but the
//     configuration did, and the same configuration is the same deployment.
//  4. A random UUID.
//
// Whichever step wins is written back to the database and the file, so the
// copies converge and the next start takes the shortest path. When the
// database and the file disagree, the database wins: the file is the copy that
// gets recreated by accident, the database the one that gets migrated on
// purpose.
//
// A store that errors is not a store that is empty. The lookup falls through
// to the file and the write-back is skipped, so a database that is briefly
// unreachable at startup can never mint a new identity.
func ResolveInstallID(ctx context.Context, store Store, secret string) (string, InstallIDSource) {
	path := platformdir.DataFile(installIDFile)
	fileID := readInstallIDFile(path)

	storeUsable := store != nil
	if storeUsable {
		id, found, err := store.Get(ctx, InstallIDKey)
		switch {
		case err != nil:
			slog.Warn("install id store unavailable; using the local copy and retrying next start", "error", err)
			storeUsable = false
		case found && strings.TrimSpace(id) != "":
			id = strings.TrimSpace(id)
			if id != fileID {
				writeInstallIDFile(path, id)
			}
			return id, SourceDatabase
		}
	}

	var id string
	source := SourceFile
	switch {
	case fileID != "":
		id = fileID
	case secret != "":
		id, source = deriveInstallID(secret), SourceDerived
	default:
		id, source = uuid.NewString(), SourceGenerated
	}

	if storeUsable {
		if err := store.Set(ctx, InstallIDKey, id); err != nil {
			slog.Warn("install id could not be saved to the database; it stays in the data directory", "error", err)
		}
	}
	if id != fileID {
		writeInstallIDFile(path, id)
	}
	return id, source
}

func readInstallIDFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// writeInstallIDFile is best-effort: a read-only or unwritable data
// directory is not an error, the caller simply has a less durable id.
func writeInstallIDFile(path, id string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// 0600: the id is not a secret, but it is per-deployment state and has
	// no reason to be world-readable.
	_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// deriveInstallID turns the operator's secret into an identifier shaped like
// every other one. HMAC-SHA256 is one-way: the identifier reveals nothing
// about the secret, and the fixed purpose string keeps it unrelated to any
// other value derived from the same secret.
func deriveInstallID(secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(derivedIDPurpose))
	var id uuid.UUID
	copy(id[:], mac.Sum(nil))
	// RFC 9562 version 8 marks a UUID whose bits are application-defined,
	// which is exactly what this is; the variant bits make it well-formed.
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}
