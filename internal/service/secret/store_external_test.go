package secret_test

import (
	"crypto/rand"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// epoch is the instant every manual clock in this suite starts at.
var epoch = time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)

// writableStore names a constructor for one of the writable stores, so the
// conformance cases below run identically against every one of them.
type writableStore struct {
	name  string
	build func(t *testing.T, clk clock.Timed) coresecret.Store
}

// writableStores lists the stores the contract is asserted against. The file
// store joins wherever it runs, which is decided by ASKING it — a probe
// construction — rather than by a GOOS list that could drift from its build
// tags; where it refuses, TestFileStoreRefusesOffItsPlatforms asserts that
// instead.
func writableStores(t *testing.T) []writableStore {
	t.Helper()
	stores := []writableStore{
		{"memory", func(_ *testing.T, clk clock.Timed) coresecret.Store {
			return svcsecret.NewMemory(svcsecret.MemoryConfig{Clock: clk})
		}},
	}
	probe, err := svcsecret.NewFile(svcsecret.FileConfig{Dir: filepath.Join(t.TempDir(), "probe")})
	//: the platform has no file store; the memory store alone carries the contract.
	if errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		return stores
	}
	if err != nil {
		t.Fatalf("probe NewFile: %v", err)
	}
	if closeErr := probe.(io.Closer).Close(); closeErr != nil {
		t.Fatalf("probe Close: %v", closeErr)
	}
	return append(stores,
		writableStore{"file", func(t *testing.T, clk clock.Timed) coresecret.Store {
			return newFileStore(t, svcsecret.FileConfig{Dir: filepath.Join(t.TempDir(), "secrets"), Clock: clk})
		}},
		writableStore{"sealed file", func(t *testing.T, clk clock.Timed) coresecret.Store {
			return newFileStore(t, svcsecret.FileConfig{Dir: filepath.Join(t.TempDir(), "secrets"), Clock: clk, Key: testKey(t)})
		}})
}

// testKey returns a fresh random 256-bit key.
func testKey(t *testing.T) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	key, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// newFileStore builds a file store and closes it when the test ends.
func newFileStore(t *testing.T, cfg svcsecret.FileConfig) coresecret.Store {
	t.Helper()
	store, err := svcsecret.NewFile(cfg)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := store.(io.Closer); ok {
			if closeErr := closer.Close(); closeErr != nil {
				t.Errorf("Close: %v", closeErr)
			}
		}
	})
	return store
}

// TestStoresNumberVersionsAndNeverReuseANumber is the version contract every
// writable store honours: numbered from 1, newest first, and a prune never
// hands a number back out.
func TestStoresNumberVersionsAndNeverReuseANumber(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c writableStore) {
		t.Helper()
		clk := clock.NewManualClock(epoch)
		store := c.build(t, clk)
		for index, text := range []string{"first", "second", "third"} {
			created, err := store.Put(t.Context(), "db-password", coresecret.FromString(text))
			if err != nil {
				t.Fatalf("Put %d: %v", index+1, err)
			}
			if created.Version != index+1 || created.Name != "db-password" || !created.Created.Equal(clk.Now()) {
				t.Fatalf("Put %d = %+v, want version %d stamped %v", index+1, created, index+1, clk.Now())
			}
			clk.Advance(time.Minute)
		}
		current, err := store.Get(t.Context(), "db-password")
		if err != nil || current.Version != 3 || current.Value.RevealString() != "third" {
			t.Fatalf("Get = (%+v %q, %v), want version 3 \"third\"", current, current.Value.RevealString(), err)
		}
		if pruneErr := store.Prune(t.Context(), "db-password", 2); pruneErr != nil {
			t.Fatalf("Prune: %v", pruneErr)
		}
		versions, err := store.Versions(t.Context(), "db-password")
		if err != nil {
			t.Fatalf("Versions: %v", err)
		}
		if got := numbers(versions); !slices.Equal(got, []int{3, 2}) {
			t.Fatalf("Versions after Prune(2) = %v, want [3 2]", got)
		}
		//: the number after a prune is past the highest ever kept.
		next, err := store.Put(t.Context(), "db-password", coresecret.FromString("fourth"))
		if err != nil || next.Version != 4 {
			t.Fatalf("Put after prune = (%d, %v), want version 4", next.Version, err)
		}
		//: a prune within the bound is not an error and changes nothing.
		if pruneErr := store.Prune(t.Context(), "db-password", 10); pruneErr != nil {
			t.Fatalf("Prune(10): %v", pruneErr)
		}
		names, err := store.Names(t.Context())
		if err != nil || !slices.Equal(names, []string{"db-password"}) {
			t.Fatalf("Names = (%v, %v), want [db-password]", names, err)
		}
	}
	for _, c := range writableStores(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStoresRefuseWhatEveryStoreRefuses pins the argument checks every store
// shares, so three backends cannot disagree about a valid call.
func TestStoresRefuseWhatEveryStoreRefuses(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c writableStore) {
		t.Helper()
		store := c.build(t, nil)
		ctx := t.Context()
		if _, err := store.Get(ctx, "absent"); !errs.HasCode(err, coresecret.CodeNotFound) {
			t.Errorf("Get(absent) = %v, want NotFound", err)
		}
		if _, err := store.Versions(ctx, "absent"); !errs.HasCode(err, coresecret.CodeNotFound) {
			t.Errorf("Versions(absent) = %v, want NotFound", err)
		}
		if err := store.Prune(ctx, "absent", 1); !errs.HasCode(err, coresecret.CodeNotFound) {
			t.Errorf("Prune(absent) = %v, want NotFound", err)
		}
		if _, err := store.Get(ctx, "Bad_Name"); !errs.HasCode(err, coresecret.CodeInvalidName) {
			t.Errorf("Get(Bad_Name) = %v, want InvalidName", err)
		}
		if _, err := store.Put(ctx, "../escape", coresecret.FromString("x")); !errs.HasCode(err, coresecret.CodeInvalidName) {
			t.Errorf("Put(../escape) = %v, want InvalidName", err)
		}
		if _, err := store.Put(ctx, "empty", coresecret.Value{}); !errs.HasCode(err, coresecret.CodeEmptyValue) {
			t.Errorf("Put(empty value) = %v, want EmptyValue", err)
		}
		if _, err := store.Put(ctx, "kept", coresecret.FromString("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Prune(ctx, "kept", 0); !errs.HasCode(err, coresecret.CodeInvalidKeep) {
			t.Errorf("Prune(keep 0) = %v, want InvalidKeep", err)
		}
		names, err := store.Names(ctx)
		if err != nil || !slices.Equal(names, []string{"kept"}) {
			t.Errorf("Names = (%v, %v): a refused Put must store nothing", names, err)
		}
	}
	for _, c := range writableStores(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStoresHandOutCopies pins that a caller mutating what it was given cannot
// change what the store holds.
func TestStoresHandOutCopies(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c writableStore) {
		t.Helper()
		store := c.build(t, nil)
		for _, text := range []string{"one", "two"} {
			if _, err := store.Put(t.Context(), "copied", coresecret.FromString(text)); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
		versions, err := store.Versions(t.Context(), "copied")
		if err != nil {
			t.Fatalf("Versions: %v", err)
		}
		versions[0] = coresecret.VersionValue{}
		versions[1].Version = 99
		again, err := store.Versions(t.Context(), "copied")
		if err != nil || !slices.Equal(numbers(again), []int{2, 1}) {
			t.Fatalf("Versions after mutating a copy = (%v, %v), want [2 1]", numbers(again), err)
		}
	}
	for _, c := range writableStores(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// numbers lists the version numbers of a history, in its order.
func numbers(versions []coresecret.VersionValue) []int {
	out := make([]int, 0, len(versions))
	for _, version := range versions {
		out = append(out, version.Version)
	}
	return out
}

// fieldText flattens every field an error carries, so a test can assert what a
// log line built from it would contain.
func fieldText(err error) string {
	var builder strings.Builder
	for _, field := range errs.FieldsOf(err) {
		builder.WriteString(field.Key())
		builder.WriteString("=")
		builder.WriteString(field.StringValue())
		builder.WriteString(" ")
	}
	return builder.String()
}

// fromText is coresecret.FromString, spelled short for the table cases.
func fromText(text string) coresecret.Value {
	return coresecret.FromString(text)
}
