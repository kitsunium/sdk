package session_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// otherKey is a second AEAD key, so "the wrong key" is a real value rather than
// a hypothetical.
func otherKey(t *testing.T) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(200 - i)
	}
	key, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// sampleID is a deterministic identifier for sealing tests.
func sampleID(t *testing.T) coresession.ID {
	t.Helper()
	raw := make([]byte, coresession.IDLen)
	for i := range raw {
		raw[i] = byte(i * 3)
	}
	id, err := coresession.NewID(raw)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// TestSealRoundTripsAndIsCookieSafe pins the sealer's whole job: produce a
// value a framework can put in a Set-Cookie header verbatim, and get the
// identifier back.
func TestSealRoundTripsAndIsCookieSafe(t *testing.T) {
	t.Parallel()
	sealer, err := svcsession.NewSealer(testKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	id := sampleID(t)
	sealed, sealErr := sealer.Seal(id)
	if sealErr != nil {
		t.Fatalf("Seal: %v", sealErr)
	}
	opened, openErr := sealer.Open(sealed)
	if openErr != nil || !opened.Equal(id) {
		t.Fatalf("Open(Seal(id)) = (%v, %v), want the same identifier", opened, openErr)
	}
	//: RFC 6265 cookie-octet: no '=', no ';', no ',', no space, no quote.
	if strings.ContainsAny(sealed, "=;, \"\\") {
		t.Errorf("sealed value %q is not a bare cookie value", sealed)
	}
	//: and it does not contain the identifier it carries.
	if strings.Contains(sealed, id.Reveal()) {
		t.Error("the sealed value contains the identifier in the clear")
	}
	//: two seals of the SAME identifier differ, because the AEAD's nonce is
	//: fresh each time — so a sealed cookie is not a stable fingerprint an
	//: observer can correlate across responses.
	again, againErr := sealer.Seal(id)
	if againErr != nil {
		t.Fatalf("Seal: %v", againErr)
	}
	if again == sealed {
		t.Error("two seals of one identifier produced identical values")
	}
}

// TestOpenIsNotAnOracle pins that every failure looks the same. Telling a
// forger which half of their attempt was already correct is how a value gets
// brute-forced one property at a time.
func TestOpenIsNotAnOracle(t *testing.T) {
	t.Parallel()
	mine, err := svcsession.NewSealer(testKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	//: same key, different purpose — the domain separator at work.
	otherPurpose, err := svcsession.NewSealer(testKey(t), "example.test/csrf")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	//: different key, same purpose.
	otherOwner, err := svcsession.NewSealer(otherKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	valid, sealErr := mine.Seal(sampleID(t))
	if sealErr != nil {
		t.Fatalf("Seal: %v", sealErr)
	}
	tampered := []byte(valid)
	tampered[len(tampered)/2] ^= 0x01
	//: a well-formed box carrying something that is not an identifier.
	notAnID, boxErr := corecrypto.Seal("aes-256-gcm", testKey(t),
		[]byte("hello"), []byte("kitsunium/sdk/session/cookie/v1|example.test/sid"))
	if boxErr != nil {
		t.Fatalf("Seal: %v", boxErr)
	}
	tests := []struct {
		name   string
		sealer coresession.Sealer
		value  string
	}{
		{"not base64", mine, "this is not base64 at all !!!"},
		{"empty", mine, ""},
		{"truncated", mine, valid[:len(valid)/2]},
		{"a flipped bit", mine, string(tampered)},
		{"the wrong purpose", otherPurpose, valid},
		{"the wrong key", otherOwner, valid},
		{"a box that is not an identifier", mine, base64.RawURLEncoding.EncodeToString(notAnID)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opened, openErr := tc.sealer.Open(tc.value)
			if !errs.HasCode(openErr, coresession.CodeSealInvalid) {
				t.Fatalf("Open = %v, want CodeSealInvalid", openErr)
			}
			//: one verdict, and no detail to learn from.
			if !errs.HasReason(openErr, "SEAL_INVALID") {
				t.Errorf("Open = %v, want reason SEAL_INVALID", openErr)
			}
			if !opened.IsZero() {
				t.Error("Open returned an identifier alongside its error")
			}
		})
	}
}

// TestSealerRefusesAnUnusableConstruction pins ADR 0031 for the sealer: an
// empty purpose is not "no separation needed", it is a missing decision.
func TestSealerRefusesAnUnusableConstruction(t *testing.T) {
	t.Parallel()
	t.Run("empty purpose", func(t *testing.T) {
		t.Parallel()
		sealer, err := svcsession.NewSealer(testKey(t), "")
		if !errs.HasCode(err, svcsession.CodeInvalidPurpose) {
			t.Fatalf("NewSealer(no purpose) = %v, want CodeInvalidPurpose", err)
		}
		if sealer != nil {
			t.Error("a refused constructor returned a sealer anyway")
		}
	})
	t.Run("zero key", func(t *testing.T) {
		t.Parallel()
		_, err := svcsession.NewSealer(corecrypto.Key{}, "example.test/sid")
		if !errs.HasCode(err, coresession.CodeInvalidConfig) {
			t.Errorf("NewSealer(zero key) = %v, want CodeInvalidConfig", err)
		}
	})
}

// TestSealRefusesTheZeroIdentifier pins that a session naming nothing is never
// rendered into a cookie a browser would then send back.
func TestSealRefusesTheZeroIdentifier(t *testing.T) {
	t.Parallel()
	sealer, err := svcsession.NewSealer(testKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, sealErr := sealer.Seal(coresession.ID{})
	if !errs.HasCode(sealErr, coresession.CodeInvalidID) {
		t.Errorf("Seal(zero) = %v, want CodeInvalidID", sealErr)
	}
	if sealed != "" {
		t.Errorf("Seal(zero) returned %q alongside its error", sealed)
	}
}

// TestOpeningIsNotAuthorising pins the boundary between the sealer and the
// store. A value that opens proves only that somebody holding the key minted
// it; whether it names a LIVE session is the store's answer, and a framework
// that stopped at Open would have built an authentication bypass out of an
// expired cookie.
func TestOpeningIsNotAuthorising(t *testing.T) {
	t.Parallel()
	sealer, err := svcsession.NewSealer(testKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	//: an identifier that never named a session at all still opens.
	orphan := sampleID(t)
	sealed, sealErr := sealer.Seal(orphan)
	if sealErr != nil {
		t.Fatalf("Seal: %v", sealErr)
	}
	opened, openErr := sealer.Open(sealed)
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}
	if !opened.Equal(orphan) {
		t.Fatal("Open did not return the identifier that was sealed")
	}
	//: and the store is the one that says no.
	store := newMemory(t, nil)
	if _, loadErr := store.Load(t.Context(), opened); !errs.HasCode(loadErr, coresession.CodeNotFound) {
		t.Errorf("Load(a sealed but unknown identifier) = %v, want CodeNotFound", loadErr)
	}
}
