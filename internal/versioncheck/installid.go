package versioncheck

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"log/slog"
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

// InstallIDKey is the key the identifier is kept under in the deployment's
// database, alongside the runtime settings.
const InstallIDKey = "install_id"

// derivedIDPurpose is the fixed message signed with the operator's secret
// when the identifier has to be derived. It ties the derivation to this use,
// so the same secret used for anything else yields an unrelated value.
const derivedIDPurpose = "gomodel install identifier v1"

// Store is the durable key/value the identifier is kept in. It is satisfied
// by runtimesettings.Store: the identifier is per-deployment state exactly
// like a runtime setting, so it shares the table.
type Store interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	// SetDefault stores value only when key has no value yet, and returns
	// whatever is stored afterwards. It must be atomic across instances.
	SetDefault(ctx context.Context, key, value string) (string, error)
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

// Identity resolves and remembers the stable, anonymous identifier for this
// deployment. It is a UUID that encodes nothing about the host, the
// operator, or the configuration, and only ever leaves the process on an
// update check.
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
// Whichever step wins is written to the database and the file, so the copies
// converge and the next start takes the shortest path. The database write is
// insert-if-absent: when two replicas initialise at once, or the file and
// the database disagree, the database's value wins for everyone. The file is
// the copy that gets recreated by accident, the database the one that gets
// migrated on purpose.
//
// A store that errors is not a store that is empty. The candidate from the
// file or the fallbacks is used for now, and the database is asked again on
// the next call, so an outage at startup can never mint a new identity or
// hide the real one for the life of the process.
type Identity struct {
	store  Store
	secret string

	mu       sync.Mutex
	id       string
	source   InstallIDSource
	settled  bool // the database has confirmed id, or there is no database
	warnedDB bool // the outage has been logged once; retries stay quiet
}

// NewIdentity prepares a resolver. store may be nil (no database) and secret
// may be empty (no derived fallback). Nothing is read until Resolve or ID.
func NewIdentity(store Store, secret string) *Identity {
	return &Identity{store: store, secret: secret}
}

// ID returns the identifier for a request, resolving it on first use.
func (i *Identity) ID(ctx context.Context) string {
	id, _ := i.Resolve(ctx)
	return id
}

// Resolve returns the identifier and where it came from. Once the database
// has confirmed the value (or there is no database to ask) the answer is
// fixed; until then each call asks again, but keeps returning the same
// provisional value rather than minting another.
func (i *Identity) Resolve(ctx context.Context) (string, InstallIDSource) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.settled {
		return i.id, i.source
	}

	path := platformdir.DataFile(installIDFile)
	fileID := readInstallIDFile(path)

	storeUsable := i.store != nil
	if storeUsable {
		stored, found, err := i.store.Get(ctx, InstallIDKey)
		switch {
		case err != nil:
			i.warnStoreOnce("install id database unavailable; using the local copy until it answers", err)
			storeUsable = false
		case found && strings.TrimSpace(stored) != "":
			i.adopt(strings.TrimSpace(stored), SourceDatabase, true)
			if i.id != fileID {
				writeInstallIDFile(path, i.id)
			}
			return i.id, i.source
		}
	}

	// The provisional id from an earlier call is kept: a database that is
	// still down must not cause a second fresh id.
	if i.id == "" {
		switch {
		case fileID != "":
			i.adopt(fileID, SourceFile, false)
		case i.secret != "":
			i.adopt(deriveInstallID(i.secret), SourceDerived, false)
		default:
			i.adopt(uuid.NewString(), SourceGenerated, false)
		}
	}

	if storeUsable {
		stored, err := i.store.SetDefault(ctx, InstallIDKey, i.id)
		switch {
		case err != nil:
			i.warnStoreOnce("install id could not be saved to the database; it stays in the data directory until it can", err)
		case strings.TrimSpace(stored) != "" && stored != i.id:
			// Another replica initialised first, or the database already
			// had an id the Get above missed: theirs is the deployment's.
			i.adopt(stored, SourceDatabase, true)
		default:
			// Ours won — or the key holds a blank value, which must never
			// become the deployment's id. Either way this id is final.
			i.settled = true
		}
	} else if i.store == nil {
		i.settled = true
	}

	if i.id != fileID {
		writeInstallIDFile(path, i.id)
	}
	return i.id, i.source
}

func (i *Identity) adopt(id string, source InstallIDSource, settled bool) {
	i.id, i.source, i.settled = id, source, settled
}

func (i *Identity) warnStoreOnce(msg string, err error) {
	if i.warnedDB {
		return
	}
	i.warnedDB = true
	slog.Warn(msg, "error", err)
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
