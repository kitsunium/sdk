package session_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

const (
	// idleWindow is the sliding half of every policy in this file.
	idleWindow time.Duration = 30 * time.Minute
	// absoluteCeiling is the hard half. It is deliberately not a multiple of
	// idleWindow, so an off-by-one-window bug shows up as a wrong instant
	// rather than as a coincidence.
	absoluteCeiling time.Duration = 100 * time.Minute
)

// origin is the fixed instant every deadline in this package is measured from.
// Nothing here reads the wall clock and nothing here sleeps: expiry is driven
// by advancing a clock.ManualClock, which makes a 12-hour ceiling assertable in
// microseconds and makes every test in this file deterministic.
var origin = time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)

// factory builds one store under test. Every contract test runs against BOTH
// implementations from the same table: a contract only one store honours is not
// a contract, and the fixation guard in particular has to be identical in a
// store that persists and one that does not.
type factory struct {
	name  string
	build func(t *testing.T, clk clock.Timed) coresession.Store
}

// factories returns the stores every contract test iterates over.
func factories() []factory {
	return []factory{
		{name: "memory", build: newMemory},
		{name: "file", build: newFile},
	}
}

// newMemory builds an in-process store on the given clock.
func newMemory(t *testing.T, clk clock.Timed) coresession.Store {
	t.Helper()
	store, err := svcsession.NewMemoryStore(svcsession.Config{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Clock: clk,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

// newFile builds an on-disk store in a fresh directory, skipping the test on a
// platform where the file store honestly refuses to exist.
func newFile(t *testing.T, clk clock.Timed) coresession.Store {
	t.Helper()
	store, err := svcsession.NewFileStore(svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Clock: clk,
		Dir: storeDir(t), Key: testKey(t),
	})
	//: ADR 0018: off the platforms with flock(2) and enforced permissions the
	//: constructor refuses, and there is nothing here to test.
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		t.Skip("file store has no native mechanic on this platform")
	}
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return store
}

// storeDir names a directory that does NOT exist yet, so the file store creates
// it and therefore owns it.
//
// The distinction is deliberate, and t.TempDir alone would not exercise it:
// t.TempDir hands back a directory that already exists, and on a filesystem
// whose parent carries a default POSIX ACL — which is the case on this
// repository's own devcontainer — that directory is group-writable. The store
// refuses it, correctly, because narrowing a directory the operator made is not
// the SDK's call. A directory the store creates is a different question, and
// this helper asks that one.
func storeDir(t *testing.T) string {
	t.Helper()
	//: one level below the temp directory, absent until NewFileStore runs.
	return filepath.Join(t.TempDir(), "sessions")
}

// testKey returns the fixed AEAD key the file store seals records with.
func testKey(t *testing.T) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	key, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// TestANewSessionIsAnonymousAndLoadable pins the starting state: a visitor gets
// a session with no subject, and the identifier they were handed resolves.
func TestANewSessionIsAnonymousAndLoadable(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if fresh.Subject() != "" {
				t.Errorf("a new session is bound to %q, want anonymous", fresh.Subject())
			}
			if fresh.ID().IsZero() {
				t.Fatal("New returned a session with no identifier")
			}
			loaded, loadErr := store.Load(ctx, fresh.ID())
			if loadErr != nil || !loaded.ID().Equal(fresh.ID()) {
				t.Fatalf("Load = (%v, %v), want the same session", loaded, loadErr)
			}
			//: two sessions never share an identifier.
			second, secondErr := store.New(ctx)
			if secondErr != nil {
				t.Fatalf("second New: %v", secondErr)
			}
			if second.ID().Equal(fresh.ID()) {
				t.Error("two New calls produced the same identifier")
			}
		})
	}
}

// TestTheIdleWindowSlidesOnLoad pins that an idle timeout is refreshed by use.
// A window that is not refreshed is an absolute timeout wearing another name,
// and the difference is invisible until a user is logged out mid-session.
func TestTheIdleWindowSlidesOnLoad(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			//: three touches, each 20 minutes apart. Without sliding the
			//: session would be dead at +30m; with it, it survives to +60m.
			for step := range 3 {
				manual.Advance(20 * time.Minute)
				loaded, loadErr := store.Load(ctx, fresh.ID())
				if loadErr != nil {
					t.Fatalf("Load after %d minutes: %v", (step+1)*20, loadErr)
				}
				want := manual.Now().Add(idleWindow)
				if !loaded.ExpiresAt().Equal(want) {
					t.Errorf("after touch %d ExpiresAt = %v, want %v", step, loaded.ExpiresAt(), want)
				}
			}
			//: and going quiet for a full window ends it.
			manual.Advance(idleWindow)
			if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeExpired) {
				t.Errorf("Load after a full idle window = %v, want CodeExpired", loadErr)
			}
		})
	}
}

// TestTheAbsoluteCeilingEndsAContinuouslyUsedSession is the contradiction case
// stated as a test: the session is touched every ten minutes forever, so the
// idle window never lapses, and it dies anyway — exactly at the ceiling.
//
// This is what makes the absolute timeout a ceiling rather than a suggestion,
// and it is the single most important behaviour in the expiry policy: a
// sliding session with no cap never dies at all.
func TestTheAbsoluteCeilingEndsAContinuouslyUsedSession(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ceiling := origin.Add(absoluteCeiling)
			//: nine touches at ten-minute intervals — the idle window never
			//: comes close to lapsing.
			for range 9 {
				manual.Advance(10 * time.Minute)
				loaded, loadErr := store.Load(ctx, fresh.ID())
				if loadErr != nil {
					t.Fatalf("Load at %v: %v", manual.Now(), loadErr)
				}
				//: and every one of those loads is CLAMPED: the sliding window
				//: would reach past the ceiling from +70m onwards, and never does.
				if loaded.ExpiresAt().After(ceiling) {
					t.Fatalf("ExpiresAt = %v, past the ceiling %v", loaded.ExpiresAt(), ceiling)
				}
			}
			//: at +100m the ceiling is reached, and use does not save it.
			manual.Advance(10 * time.Minute)
			if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeExpired) {
				t.Errorf("Load at the ceiling = %v, want CodeExpired", loadErr)
			}
		})
	}
}

// TestAnExpiredRecordIsDroppedAsItIsReported pins that the second call answers
// NotFound. A dead record that stayed in the store could be resurrected by a
// clock that moves backwards — an NTP step, a VM restore — and would grow the
// store forever.
func TestAnExpiredRecordIsDroppedAsItIsReported(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			manual.Advance(absoluteCeiling)
			if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeExpired) {
				t.Fatalf("first Load = %v, want CodeExpired", loadErr)
			}
			//: the clock moves BACK, which is what an NTP correction looks like.
			manual.Set(origin)
			if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeNotFound) {
				t.Errorf("Load after the clock moved back = %v, want CodeNotFound", loadErr)
			}
		})
	}
}

// TestDestroyIsImmediateAndIdempotent pins the property a session has and a
// token does not: revocation that takes effect on the next request.
func TestDestroyIsImmediateAndIdempotent(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if destroyErr := store.Destroy(ctx, fresh.ID()); destroyErr != nil {
				t.Fatalf("Destroy: %v", destroyErr)
			}
			//: no clock has moved; the session is simply gone.
			if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeNotFound) {
				t.Errorf("Load after Destroy = %v, want CodeNotFound", loadErr)
			}
			//: logging out twice is not a fault — reporting one would push every
			//: caller into ignoring the error.
			if destroyErr := store.Destroy(ctx, fresh.ID()); destroyErr != nil {
				t.Errorf("second Destroy = %v, want nil", destroyErr)
			}
			//: and neither is destroying nothing at all.
			if destroyErr := store.Destroy(ctx, coresession.ID{}); destroyErr != nil {
				t.Errorf("Destroy(zero) = %v, want nil", destroyErr)
			}
		})
	}
}

// TestTheZeroIdentifierIsRefusedEverywhere pins that a session that names
// nothing never reaches a lookup, a filesystem path, or a write.
func TestTheZeroIdentifierIsRefusedEverywhere(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			if _, err := store.Load(ctx, coresession.ID{}); !errs.HasCode(err, coresession.CodeInvalidID) {
				t.Errorf("Load(zero) = %v, want CodeInvalidID", err)
			}
			if _, err := store.Regenerate(ctx, coresession.ID{}, "alice"); !errs.HasCode(err, coresession.CodeInvalidID) {
				t.Errorf("Regenerate(zero) = %v, want CodeInvalidID", err)
			}
			if err := store.Save(ctx, coresession.SessionValue{}); !errs.HasCode(err, coresession.CodeInvalidID) {
				t.Errorf("Save(zero) = %v, want CodeInvalidID", err)
			}
		})
	}
}

// TestSweepDropsOnlyDeadRecords pins the sibling capability, and pins that it
// is reached by type assertion rather than through the frozen port.
func TestSweepDropsOnlyDeadRecords(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			doomed, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			//: one idle window later the first session has lapsed, and a second
			//: is born into the same store.
			manual.Advance(idleWindow + time.Minute)
			survivor, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			sweeper, ok := store.(coresession.Sweeper)
			if !ok {
				t.Fatal("store does not implement Sweeper")
			}
			removed, sweepErr := sweeper.Sweep(ctx)
			if sweepErr != nil {
				t.Fatalf("Sweep: %v", sweepErr)
			}
			if removed != 1 {
				t.Errorf("Sweep removed %d records, want 1", removed)
			}
			if _, loadErr := store.Load(ctx, doomed.ID()); !errs.HasCode(loadErr, coresession.CodeNotFound) {
				t.Errorf("the expired session survived the sweep: %v", loadErr)
			}
			if _, loadErr := store.Load(ctx, survivor.ID()); loadErr != nil {
				t.Errorf("the live session did not survive the sweep: %v", loadErr)
			}
		})
	}
}
