package secret

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestAWrappedValueThatIsNoKeyIsUnreadable pins the length check behind the
// unwrap: bytes wrapped under the root's wrap key that are not one key long —
// which this engine never wraps — are refused as unreadable, never used as a
// key.
func TestAWrappedValueThatIsNoKeyIsUnreadable(t *testing.T) {
	t.Parallel()
	roots := NewMemory(MemoryConfig{})
	generate := Random(32)
	value, err := generate()
	if err != nil {
		t.Fatalf("Random: %v", err)
	}
	if _, putErr := roots.Put(t.Context(), "data-key", value); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	root, err := NewKeyring(roots, "data-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	short, err := root.sealAs(t.Context(), wrapLabel, []byte("sixteen byte key"), wrapAAD("user:1"))
	if err != nil {
		t.Fatalf("sealAs: %v", err)
	}
	store := NewMemorySubjectKeyStore()
	if inserted, insertErr := store.Insert(t.Context(), "user:1", short); !inserted || insertErr != nil {
		t.Fatalf("Insert = (%v, %v)", inserted, insertErr)
	}
	keys, err := NewSubjectKeys(SubjectKeysConfig{Root: root, Store: store})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	if _, sealErr := keys.Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Seal over a short key = %v, want SubjectKeyUnreadable", sealErr)
	}
}

// TestAShortKeyUnderAnOldVersionIsNeverMoved pins the length check in a pass:
// a wrapper that authenticates under an old root version but does not hold
// one key is counted unreadable and left as it is — never re-wrapped and
// reported as moved — and it pins no version.
func TestAShortKeyUnderAnOldVersionIsNeverMoved(t *testing.T) {
	t.Parallel()
	roots := NewMemory(MemoryConfig{})
	generate := Random(32)
	//: two versions: the short key goes under the older one.
	for range 2 {
		value, err := generate()
		if err != nil {
			t.Fatalf("Random: %v", err)
		}
		if _, putErr := roots.Put(t.Context(), "data-key", value); putErr != nil {
			t.Fatalf("Put: %v", putErr)
		}
	}
	root, err := NewKeyring(roots, "data-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	view, err := root.view(t.Context(), wrapLabel)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	short, err := root.sealUnder(view.keys[1], 1, []byte("sixteen byte key"), wrapAAD("user:1"))
	view.close()
	if err != nil {
		t.Fatalf("sealUnder: %v", err)
	}
	store := NewMemorySubjectKeyStore()
	if inserted, insertErr := store.Insert(t.Context(), "user:1", short); !inserted || insertErr != nil {
		t.Fatalf("Insert = (%v, %v)", inserted, insertErr)
	}
	keys, err := NewSubjectKeys(SubjectKeysConfig{Root: root, Store: store})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	report, err := keys.Rewrap(t.Context())
	if report != (RewrapValue{Root: 2, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap = (%+v, %v), want the short key counted unreadable", report, err)
	}
	if held, _, getErr := store.Get(t.Context(), "user:1"); getErr != nil || string(held) != string(short) {
		t.Fatal("the pass rewrote a key that holds no data key")
	}
	if oldest, oldestErr := keys.OldestRoot(t.Context()); oldest != 0 || oldestErr != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (0, nil): a key that is no key pins nothing", oldest, oldestErr)
	}
}

// countingKeyStore counts the reads of a subject key store, so a test tells a
// key served from the cache from one read again.
type countingKeyStore struct {
	coresecret.SubjectKeyStore
	gets atomic.Int64
}

// Get counts the read and forwards it.
func (c *countingKeyStore) Get(ctx context.Context, subject string) (wrapped []byte, found bool, err error) {
	c.gets.Add(1)
	return c.SubjectKeyStore.Get(ctx, subject)
}

// TestExpireWipesAnIdleSubjectsKeyWithNoCallAtAll pins #1038's caller-driven
// sweep: with no Seal nor Open on any subject, Expire alone wipes an opened key
// whose CacheTTL passed — its bytes zeroed in place, its entry gone — and the
// next Open of its box reads the store again instead of the cache. A sweep
// before the deadline, and one on an engine without a cache, wipe nothing.
func TestExpireWipesAnIdleSubjectsKeyWithNoCallAtAll(t *testing.T) {
	t.Parallel()
	roots := NewMemory(MemoryConfig{})
	value, err := Random(32)()
	if err != nil {
		t.Fatalf("Random: %v", err)
	}
	if _, putErr := roots.Put(t.Context(), "data-key", value); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	root, err := NewKeyring(roots, "data-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	store := &countingKeyStore{SubjectKeyStore: NewMemorySubjectKeyStore()}
	clk := clock.NewManualClock(time.Date(2026, time.November, 3, 0, 0, 0, 0, time.UTC))
	keys, err := NewSubjectKeys(SubjectKeysConfig{Root: root, Store: store, CacheSize: 8, CacheTTL: time.Minute, Clock: clk})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	box, err := keys.Seal(t.Context(), "user:idle", []byte("payload"), "record")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, cached := keys.cache.entries.Fetch("user:idle")
	if !cached {
		t.Fatal("Seal left no opened key in the cache")
	}
	shared := opened.seal
	if wiped := keys.Expire(); wiped != 0 {
		t.Fatalf("Expire before the TTL wiped %d keys, want 0", wiped)
	}
	clk.Advance(time.Minute + time.Second)
	if wiped := keys.Expire(); wiped != 1 {
		t.Fatalf("Expire past the TTL wiped %d keys, want 1", wiped)
	}
	if !bytes.Equal(shared, make([]byte, len(shared))) {
		t.Fatalf("the swept key's bytes are %x, want zeros", shared)
	}
	if _, _, live := opened.take(); live {
		t.Fatal("the swept key still hands out its AEAD key")
	}
	if held := keys.cache.entries.Len(); held != 0 {
		t.Fatalf("the cache holds %d entries after the sweep, want 0", held)
	}
	reads := store.gets.Load()
	plaintext, err := keys.Open(t.Context(), box, "record")
	if err != nil || string(plaintext) != "payload" {
		t.Fatalf("Open after the sweep = (%q, %v), want the payload", plaintext, err)
	}
	if store.gets.Load() == reads {
		t.Fatal("Open after the sweep was served from the cache, not read from the store")
	}
	if wiped := keys.Expire(); wiped != 0 {
		t.Fatalf("Expire right after a fresh read wiped %d keys, want 0", wiped)
	}
	uncached, err := NewSubjectKeys(SubjectKeysConfig{Root: root, Store: store})
	if err != nil {
		t.Fatalf("NewSubjectKeys without a cache: %v", err)
	}
	if wiped := uncached.Expire(); wiped != 0 {
		t.Fatalf("Expire without a cache wiped %d keys, want 0", wiped)
	}
}
