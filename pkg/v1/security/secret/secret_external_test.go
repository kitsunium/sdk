package secret_test

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// TestKeyringAcrossRotationsThroughTheFacade drives the whole of it through
// the public names only: a file store, a rotator on a manual clock, a keyring
// that keeps opening old boxes until they are pruned.
func TestKeyringAcrossRotationsThroughTheFacade(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	store := secret.NewMemory(secret.MemoryConfig{Clock: clk})
	rotator, err := secret.NewRotator(secret.RotatorConfig{
		Store: store, Name: "cookie-key", Clock: clk,
		Policy: secret.Policy{Every: time.Hour, Keep: 2, Generate: secret.Random(32)},
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	if _, err := rotator.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	keyring, err := secret.NewKeyring(store, "cookie-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	box, err := keyring.Seal(t.Context(), []byte("session 42"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	clk.Advance(time.Hour)
	if _, rotated, err := rotator.RotateIfDue(t.Context()); err != nil || !rotated {
		t.Fatalf("RotateIfDue = (%v, %v), want a rotation", rotated, err)
	}
	if opened, err := keyring.Open(t.Context(), box, nil); err != nil || string(opened) != "session 42" {
		t.Fatalf("Open after one rotation = (%q, %v)", opened, err)
	}
	clk.Advance(time.Hour)
	if _, err := rotator.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	//: Keep 2: the version that sealed the box is gone.
	if _, err := keyring.Open(t.Context(), box, nil); !errs.HasCode(err, secret.SealInvalid.Code()) {
		t.Fatalf("Open after the sealing version was pruned = %v, want SealInvalid", err)
	}
}

// TestStoresThroughTheFacade pins the three constructors' public contracts in
// one place: the environment is read-only, a name outside the grammar is
// refused everywhere, and the file store's zero configuration is refused.
func TestStoresThroughTheFacade(t *testing.T) {
	t.Parallel()
	env, err := secret.NewEnv(secret.EnvConfig{Prefix: "FACADE_TEST_UNSET"})
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	if _, err := env.Put(t.Context(), "x", secret.FromString("y")); !errs.HasCode(err, secret.ReadOnly.Code()) {
		t.Errorf("env Put = %v, want ReadOnly", err)
	}
	if err := secret.ValidateName("Not_Valid"); !errs.HasCode(err, secret.InvalidName.Code()) {
		t.Errorf("ValidateName = %v, want InvalidName", err)
	}
	if _, err := secret.NewFile(secret.FileConfig{}); !errs.HasCode(err, secret.InvalidConfig.Code()) {
		t.Errorf("NewFile(zero) = %v, want InvalidConfig", err)
	}
	value := secret.New([]byte{0, 1, 2})
	if value.Len() != 3 || fmt.Sprint(value) != secret.Redacted || secret.MaxNameLen != 63 {
		t.Errorf("New/Redacted/MaxNameLen do not agree with the domain")
	}
}

// TestKeyFileThroughTheFacade pins the public contract where the platform has
// a file store — concurrent first uses agree on one key — and the refusal
// where it does not.
func TestKeyFileThroughTheFacade(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "keys", "store.key")
	first, err := secret.KeyFile(path)
	if errs.HasCode(err, proc.UnsupportedPlatform.Code()) {
		return
	}
	if err != nil {
		t.Fatalf("KeyFile: %v", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			again, againErr := secret.KeyFile(path)
			if againErr != nil || !bytes.Equal(again.Bytes(), first.Bytes()) {
				t.Errorf("a concurrent KeyFile returned another key (%v)", againErr)
			}
		})
	}
	wg.Wait()
	store, err := secret.NewFile(secret.FileConfig{Dir: filepath.Join(filepath.Dir(path), "store"), Key: first})
	if err != nil {
		t.Fatalf("NewFile with the key: %v", err)
	}
	if closer, ok := store.(io.Closer); ok {
		if closeErr := closer.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}
}
