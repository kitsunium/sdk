package crypto_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failingAEAD is fakeAEAD whose Seal always refuses: the error path of a
// scheme NewSealer wraps.
type failingAEAD struct{ fakeAEAD }

func (failingAEAD) Seal(crypto.Key, []byte, []byte) ([]byte, error) {
	return nil, crypto.EntropyFailed
}

// bindingAEAD is fakeAEAD with a Binder: NewSealer must hand back its sealer.
type bindingAEAD struct{ fakeAEAD }

// boundMark is the Sealer bindingAEAD binds, recognisable by type.
type boundMark struct{}

func (boundMark) SealTo(dst, plaintext, _ []byte) ([]byte, error) {
	return append(dst, plaintext...), nil
}

func (bindingAEAD) Bind(crypto.Key) (crypto.Sealer, error) { return boundMark{}, nil }

// TestNewSealer pins #813's NewSealer: an unregistered name is
// UnknownAlgorithm; a scheme with a Binder binds itself; any other is
// wrapped so SealTo appends what its Seal returns, which Open opens, and a
// Seal failure leaves dst unextended.
func TestNewSealer(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	crypto.ResetForTest()
	crypto.Register(fakeAEAD{name: "sealer-fake", id: 0x51})
	crypto.Register(failingAEAD{fakeAEAD{name: "sealer-failing", id: 0x52}})
	crypto.Register(bindingAEAD{fakeAEAD{name: "sealer-binding", id: 0x53}})
	key := testKey(t)
	if _, err := crypto.NewSealer("ghost-scheme", key); !errs.HasCode(err, crypto.CodeUnknownAlgorithm) {
		t.Fatalf("NewSealer(unregistered) err=%v want UnknownAlgorithm", err)
	}
	if s, err := crypto.NewSealer("sealer-binding", key); err != nil {
		t.Fatalf("NewSealer(binding) err=%v", err)
	} else if _, own := s.(boundMark); !own {
		t.Fatalf("NewSealer(binding) = %T, want the scheme's own Sealer", s)
	}
	sealer, err := crypto.NewSealer("sealer-fake", key)
	if err != nil {
		t.Fatalf("NewSealer(fake) err=%v", err)
	}
	prefix := []byte{0xAA, 0xBB}
	out, err := sealer.SealTo(prefix, []byte("payload"), nil)
	if err != nil || !bytes.Equal(out[:2], prefix) {
		t.Fatalf("SealTo = (%x, %v), want the box after the prefix", out, err)
	}
	if got, oerr := crypto.Open(key, out[2:], nil); oerr != nil || string(got) != "payload" {
		t.Fatalf("Open(SealTo's box) = (%q, %v), want payload", got, oerr)
	}
	failing, err := crypto.NewSealer("sealer-failing", key)
	if err != nil {
		t.Fatalf("NewSealer(failing) err=%v", err)
	}
	if out, err := failing.SealTo(prefix, []byte("x"), nil); !errs.HasCode(err, crypto.CodeEntropyFailed) || len(out) != len(prefix) {
		t.Fatalf("SealTo of a failing scheme = (%x, %v), want the prefix alone and its error", out, err)
	}
}
