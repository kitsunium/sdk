package secret

import (
	"testing"

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
	if _, sealErr := keys.Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, CodeSubjectKeyUnreadable) {
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
	if report != (RewrapValue{Root: 2, Unreadable: 1}) || !errs.HasCode(err, CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap = (%+v, %v), want the short key counted unreadable", report, err)
	}
	if held, _, getErr := store.Get(t.Context(), "user:1"); getErr != nil || string(held) != string(short) {
		t.Fatal("the pass rewrote a key that holds no data key")
	}
	if oldest, oldestErr := keys.OldestRoot(t.Context()); oldest != 0 || oldestErr != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (0, nil): a key that is no key pins nothing", oldest, oldestErr)
	}
}
