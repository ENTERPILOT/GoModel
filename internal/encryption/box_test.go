package encryption

import (
	"bytes"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBox(t *testing.T, active string, ids ...string) *Box {
	t.Helper()
	deks := map[string][]byte{}
	for _, id := range append([]string{active}, ids...) {
		dek := make([]byte, dekSize)
		_, err := rand.Read(dek)
		require.NoError(t, err)
		deks[id] = dek
	}
	box, err := newBox(active, deks)
	require.NoError(t, err)
	return box
}

func TestBoxSealOpenRoundTrip(t *testing.T) {
	box := testBox(t, "1")
	aad := AAD("provider_credential", "openai", "api_keys")

	sealed, err := box.Seal(aad, "sk-secret")
	require.NoError(t, err)
	assert.Regexp(t, `^enc:v1:1:[A-Za-z0-9+/]+=*$`, sealed)
	assert.NotContains(t, sealed, "sk-secret")
	assert.True(t, IsSealed(sealed))
	assert.True(t, box.IsCurrent(sealed))

	again, err := box.Seal(aad, "sk-secret")
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "every seal must use a fresh nonce")

	opened, err := box.Open(aad, sealed)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", opened)
}

func TestBoxOpenRejects(t *testing.T) {
	box := testBox(t, "1")
	aad := AAD("mcp_server", "github", "headers.Authorization")
	sealed, err := box.Seal(aad, "Bearer x")
	require.NoError(t, err)

	tests := []struct {
		name  string
		aad   []byte
		value string
		want  string
	}{
		{"other entity", AAD("mcp_server", "gitlab", "headers.Authorization"), sealed, "moved from another row or field"},
		{"other field", AAD("mcp_server", "github", "headers.X-Token"), sealed, "moved from another row or field"},
		{"other kind", AAD("guardrail", "github", "headers.Authorization"), sealed, "moved from another row or field"},
		{"unknown key", aad, strings.Replace(sealed, "enc:v1:1:", "enc:v1:9:", 1), "not in encryption_keys"},
		{"no payload", aad, "enc:v1:1:", "malformed"},
		{"bad base64", aad, "enc:v1:1:!!!", "malformed"},
		{"too short", aad, "enc:v1:1:AAAA", "malformed"},
		{"tampered", aad, sealed[:len(sealed)-4] + "AAA=", "decryption failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := box.Open(tt.aad, tt.value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Empty(t, got)
		})
	}

	other := testBox(t, "1")
	_, err = other.Open(aad, sealed)
	require.Error(t, err, "a different data key with the same id must not open the value")
}

func TestBoxPlaintextPassesThrough(t *testing.T) {
	for name, box := range map[string]*Box{"enabled": testBox(t, "1"), "disabled": Disabled(), "nil": nil} {
		t.Run(name, func(t *testing.T) {
			opened, err := box.Open(AAD("k", "id", "f"), "legacy-plaintext")
			require.NoError(t, err)
			assert.Equal(t, "legacy-plaintext", opened)

			empty, err := box.Seal(AAD("k", "id", "f"), "")
			require.NoError(t, err)
			assert.Empty(t, empty, "empty values stay empty")
		})
	}
}

func TestDisabledBox(t *testing.T) {
	box := Disabled()
	assert.False(t, box.Enabled())
	assert.Empty(t, box.ActiveKeyID())

	sealed, err := box.Seal(AAD("k", "id", "f"), "secret")
	require.NoError(t, err)
	assert.Equal(t, "secret", sealed)

	_, err = box.Open(AAD("k", "id", "f"), "enc:v1:1:AAAA")
	require.ErrorIs(t, err, ErrKeyRequired)
	assert.False(t, box.NeedsReseal(Field{Name: "f", Value: &sealed}))
}

func TestBoxIsCurrentAndNeedsReseal(t *testing.T) {
	old := testBox(t, "1")
	sealedOld, err := old.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)

	box := testBox(t, "2", "1")
	sealedNew, err := box.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)
	plain, empty := "v", ""

	assert.False(t, box.IsCurrent(sealedOld))
	assert.True(t, box.IsCurrent(sealedNew))
	assert.False(t, box.IsCurrent(plain))

	tests := []struct {
		name  string
		value *string
		want  bool
	}{
		{"current", &sealedNew, false},
		{"empty", &empty, false},
		{"plaintext", &plain, true},
		{"older key", &sealedOld, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, box.NeedsReseal(Field{Name: "f", Value: tt.value}))
		})
	}
}

func TestSealOpenFields(t *testing.T) {
	box := testBox(t, "1")
	a, b := "alpha", ""
	fields := []Field{{Name: "a", Value: &a}, {Name: "b", Value: &b}}
	require.NoError(t, box.SealFields("kind", "row", fields...))
	assert.True(t, IsSealed(a))
	assert.Empty(t, b)

	require.NoError(t, box.OpenFields("kind", "row", fields...))
	assert.Equal(t, "alpha", a)

	require.NoError(t, box.SealFields("kind", "row", fields...))
	err := box.OpenFields("kind", "other-row", fields...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `kind "other-row" field a`)
	assert.NotContains(t, err.Error(), "alpha")
}

func TestPlaintextWarningLogsOnce(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	box := Disabled()
	for range 3 {
		_, err := box.Open(AAD("k", "id", "f"), "plaintext-secret")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "stored in plaintext"))
	assert.Contains(t, logs.String(), "GOMODEL_ENCRYPTION_KEY")
	assert.NotContains(t, logs.String(), "plaintext-secret")

	logs.Reset()
	_, err := Disabled().Open(AAD("k", "id", "f"), "")
	require.NoError(t, err)
	assert.Empty(t, logs.String(), "empty values are not secrets")
}

func TestBoxReloadsKeysForUnknownKeyID(t *testing.T) {
	stale := testBox(t, "1")
	fresh := testBox(t, "2", "1")
	fresh.keys["1"] = stale.keys["1"]
	sealed, err := fresh.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)

	reloads := 0
	stale.reload = func() (*Box, error) {
		reloads++
		return fresh, nil
	}
	opened, err := stale.Open(AAD("k", "id", "f"), sealed)
	require.NoError(t, err)
	assert.Equal(t, "v", opened)
	assert.Equal(t, 1, reloads)
	assert.Equal(t, "2", stale.ActiveKeyID(), "new values are sealed with the rotated key")

	unknown := strings.Replace(sealed, "enc:v1:2:", "enc:v1:7:", 1)
	_, err = stale.Open(AAD("k", "id", "f"), unknown)
	require.ErrorIs(t, err, errUnknownKey)
	_, err = stale.Open(AAD("k", "id", "f"), unknown)
	require.ErrorIs(t, err, errUnknownKey)
	assert.Equal(t, 1, reloads, "reloads are rate limited")
}

func TestReencryptRows(t *testing.T) {
	attempts := map[string]int{}
	outcomes := map[string][]RowOutcome{
		"rewritten": {RowRewritten},
		"unchanged": {RowUnchanged},
		"retried":   {RowConflict, RowRewritten},
		"stuck":     {RowConflict, RowConflict, RowConflict, RowRewritten},
	}
	report, err := ReencryptRows("t", []string{"rewritten", "unchanged", "retried", "stuck"}, func(name string) (RowOutcome, error) {
		outcome := outcomes[name][attempts[name]]
		attempts[name]++
		return outcome, nil
	})
	require.NoError(t, err)
	assert.Equal(t, Report{Entity: "t", Rows: 4, Reencrypted: 2, Skipped: 1}, report)
	assert.Equal(t, 3, attempts["stuck"], "a row in conflict is retried a bounded number of times")
}

func TestBoxReloadDoesNotBlockKnownKeys(t *testing.T) {
	box := testBox(t, "1")
	known, err := box.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)

	started, release := make(chan struct{}), make(chan struct{})
	var activeChecks atomic.Int32
	box.activeID = func() (string, error) {
		activeChecks.Add(1)
		return "1", nil
	}
	box.reload = func() (*Box, error) {
		close(started)
		<-release
		return nil, errors.New("key store unavailable")
	}
	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		_, err := box.Open(AAD("k", "id", "f"), strings.Replace(known, "enc:v1:1:", "enc:v1:9:", 1))
		assert.ErrorIs(t, err, errUnknownKey)
	}()
	<-started

	done := make(chan struct{})
	go func() {
		defer close(done)
		opened, err := box.Open(AAD("k", "id", "f"), known)
		assert.NoError(t, err)
		assert.Equal(t, "v", opened)
		_, err = box.Seal(AAD("k", "id", "f"), "w")
		assert.NoError(t, err)
		assert.Equal(t, "1", box.ActiveKeyID())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		assert.Fail(t, "reads and seals waited for a slow key-store reload")
	}
	close(release)
	<-reloadDone
	assert.Positive(t, activeChecks.Load(), "the seal went through the key-store active-key check")
}

func TestQueuedReloadsReuseTheOneInProgress(t *testing.T) {
	stale := testBox(t, "1")
	fresh := testBox(t, "2", "1")
	started, release := make(chan struct{}), make(chan struct{})
	var reloads atomic.Int32
	stale.reload = func() (*Box, error) {
		if reloads.Add(1) == 1 {
			close(started)
			<-release
		}
		return fresh, nil
	}

	first := make(chan bool)
	go func() { first <- stale.reloadKeys(true) }()
	<-started
	queued := make(chan bool, 5)
	for range 5 {
		go func() { queued <- stale.reloadKeys(true) }()
	}
	time.Sleep(50 * time.Millisecond) // let them queue behind the first
	close(release)
	assert.True(t, <-first)
	for range 5 {
		assert.True(t, <-queued)
	}
	assert.Equal(t, int32(1), reloads.Load(), "saves queued behind a reload reuse it")
}

func TestRotatedSinceSealReportsActiveKeyFailures(t *testing.T) {
	box := testBox(t, "1")
	sealed, err := box.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)
	field := Field{Name: "f", Value: &sealed}

	box.activeID = func() (string, error) { return "", errors.New("key store unavailable") }
	_, err = box.RotatedSinceSeal(field)
	require.Error(t, err, "a failed check is not reported as no rotation")

	box.activeID = func() (string, error) { return "2", nil }
	rotated, err := box.RotatedSinceSeal(field)
	require.NoError(t, err)
	assert.True(t, rotated)
}

func TestConfirmSeal(t *testing.T) {
	stale := testBox(t, "1")
	sealed, err := stale.Seal(AAD("k", "id", "f"), "v")
	require.NoError(t, err)
	field := Field{Name: "f", Value: &sealed}

	t.Run("retries once after a forced reload", func(t *testing.T) {
		box := testBox(t, "1")
		failures, reloads := 1, 0
		box.activeID = func() (string, error) {
			if failures > 0 {
				failures--
				return "", errors.New("key store unavailable")
			}
			return "1", nil
		}
		box.reload = func() (*Box, error) {
			reloads++
			return testBox(t, "1"), nil
		}
		resealed := false
		require.NoError(t, box.ConfirmSeal(func() error { resealed = true; return nil }, field))
		assert.Equal(t, 1, reloads)
		assert.False(t, resealed, "the key did not change")
	})

	t.Run("reports a check that keeps failing", func(t *testing.T) {
		box := testBox(t, "1")
		box.activeID = func() (string, error) { return "", errors.New("key store unavailable") }
		box.reload = func() (*Box, error) { return nil, errors.New("key store unavailable") }
		err := box.ConfirmSeal(func() error { return nil }, field)
		require.ErrorIs(t, err, ErrSealUnconfirmed)
		assert.NotContains(t, err.Error(), sealed)
	})

	t.Run("reseals after a rotation and reports a failed reseal", func(t *testing.T) {
		box := testBox(t, "1")
		box.activeID = func() (string, error) { return "2", nil }
		calls := 0
		require.NoError(t, box.ConfirmSeal(func() error { calls++; return nil }, field))
		assert.Equal(t, 1, calls)
		err := box.ConfirmSeal(func() error { return errors.New("swap failed") }, field)
		require.ErrorIs(t, err, ErrSealUnconfirmed)
	})
}

func TestQueuedReloadsDoNotReuseAFailedOne(t *testing.T) {
	stale := testBox(t, "1")
	fresh := testBox(t, "2", "1")
	started, release := make(chan struct{}), make(chan struct{})
	var reloads atomic.Int32
	stale.reload = func() (*Box, error) {
		if reloads.Add(1) == 1 {
			close(started)
			<-release
			return nil, errors.New("key store unavailable")
		}
		return fresh, nil
	}

	first := make(chan bool)
	go func() { first <- stale.reloadKeys(true) }()
	<-started
	queued := make(chan bool)
	go func() { queued <- stale.reloadKeys(true) }()
	time.Sleep(50 * time.Millisecond) // let it queue behind the first
	close(release)
	assert.False(t, <-first)
	assert.True(t, <-queued, "a caller queued behind a failed reload reloads itself")
	assert.Equal(t, int32(2), reloads.Load())
	assert.Equal(t, "2", stale.ActiveKeyID())
	assert.False(t, stale.reloadKeys(false), "failures and successes still rate-limit unforced reloads")
}
