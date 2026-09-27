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
