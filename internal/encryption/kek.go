package encryption

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Wrapper wraps and unwraps data keys with a key-encryption key held
// elsewhere, such as a KMS. It has the method set of config.KeyWrapper, so an
// extension's wrapper is passed straight through.
type Wrapper interface {
	ID() string
	WrapKey(ctx context.Context, dek []byte) ([]byte, error)
	UnwrapKey(ctx context.Context, wrapped []byte) ([]byte, error)
}

// LocalWrapperID is the wrapper id recorded for data keys wrapped with the
// KEK derived from GOMODEL_ENCRYPTION_KEY.
const LocalWrapperID = "local"

// kdfArgon2id names the key derivation recorded with locally wrapped keys.
const kdfArgon2id = "argon2id"

const saltSize = 16

// kek wraps one data key row. The local KEK needs the row's salt and KDF
// parameters, which an extension Wrapper does not have, so both are adapted
// to this shape.
type kek interface {
	id() string
	wrap(ctx context.Context, key *Key, dek []byte) error
	unwrap(ctx context.Context, key Key) ([]byte, error)
}

// localKEK derives the KEK from GOMODEL_ENCRYPTION_KEY with Argon2id, using
// the salt and parameters stored with each data key.
type localKEK struct {
	secret string
	params kdfParams
}

func newLocalKEK(secret string) *localKEK {
	return &localKEK{secret: secret, params: defaultKDFParams}
}

func (k *localKEK) id() string { return LocalWrapperID }

func (k *localKEK) wrap(_ context.Context, key *Key, dek []byte) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	aead, err := newAEAD(k.params.derive(k.secret, salt))
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	key.Wrapped = aead.Seal(nonce, nonce, dek, wrapAAD(key.ID))
	key.WrapperID = LocalWrapperID
	key.KDF = kdfArgon2id
	key.KDFParams = k.params.String()
	key.Salt = salt
	return nil
}

func (k *localKEK) unwrap(_ context.Context, key Key) ([]byte, error) {
	if key.KDF != kdfArgon2id {
		return nil, fmt.Errorf("unsupported key derivation %q", key.KDF)
	}
	params, err := parseKDFParams(key.KDFParams)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(params.derive(k.secret, key.Salt))
	if err != nil {
		return nil, err
	}
	if len(key.Wrapped) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("wrapped data key is malformed")
	}
	nonce, ciphertext := key.Wrapped[:aead.NonceSize()], key.Wrapped[aead.NonceSize():]
	dek, err := aead.Open(nil, nonce, ciphertext, wrapAAD(key.ID))
	if err != nil {
		return nil, errWrongKey
	}
	return dek, nil
}

// errWrongKey reports a KEK that does not unwrap a data key.
var errWrongKey = errors.New("key does not match")

// wrapAAD binds a wrapped data key to its id.
func wrapAAD(keyID string) []byte {
	return []byte("gomodel-dek\x00" + keyID)
}

// extensionKEK adapts an extension's Wrapper.
type extensionKEK struct {
	wrapper Wrapper
}

func (k extensionKEK) id() string { return k.wrapper.ID() }

func (k extensionKEK) wrap(ctx context.Context, key *Key, dek []byte) error {
	wrapped, err := k.wrapper.WrapKey(ctx, dek)
	if err != nil {
		return err
	}
	key.Wrapped = wrapped
	key.WrapperID = k.wrapper.ID()
	key.KDF, key.KDFParams, key.Salt = "", "", nil
	return nil
}

func (k extensionKEK) unwrap(ctx context.Context, key Key) ([]byte, error) {
	dek, err := k.wrapper.UnwrapKey(ctx, key.Wrapped)
	if err != nil {
		return nil, err
	}
	if len(dek) != dekSize {
		return nil, fmt.Errorf("unwrapped data key is %d bytes, want %d", len(dek), dekSize)
	}
	return dek, nil
}

// derive runs Argon2id with these parameters.
func (p kdfParams) derive(secret string, salt []byte) []byte {
	return argon2.IDKey([]byte(secret), salt, p.Time, p.MemoryKiB, p.Threads, dekSize)
}
