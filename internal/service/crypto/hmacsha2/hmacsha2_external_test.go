package hmacsha2_test

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
)

func macKey(t *testing.T, b byte) corecrypto.Key {
	t.Helper()
	//: deterministic 32-byte key for repeatable tags.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{b}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

func TestMAC_throughRegistry(t *testing.T) {
	t.Parallel()
	const (
		caseGenuine  int = iota // genuine tag, same key
		caseTamper              // a tag byte flipped
		caseWrongKey            // verify under a different key
	)
	type tc struct {
		name    string
		mode    int
		message string
		wantOK  bool
	}
	tests := []tc{
		{"genuine tag verifies", caseGenuine, "authentic message", true},
		{"tampered tag fails", caseTamper, "authentic message", false},
		{"wrong key fails", caseWrongKey, "authentic message", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: importing the package self-registered it, so the registry resolves.
		if hmacsha2.MAC.Algorithm() != corecrypto.Algorithm("hmac-sha256") {
			t.Fatalf("Algorithm()=%q want hmac-sha256", hmacsha2.MAC.Algorithm())
		}
		key := macKey(t, 0x1)
		//: dispatch the tag through the core MACTag verb.
		tag, err := corecrypto.MACTag("hmac-sha256", key, []byte(c.message))
		if err != nil {
			t.Fatalf("MACTag: %v", err)
		}
		//: tamper a tag byte to drive the negative path.
		if c.mode == caseTamper {
			tag[0] ^= 0xff
		}
		//: verify under a distinct key to drive the wrong-key path.
		vkey := key
		if c.mode == caseWrongKey {
			vkey = macKey(t, 0x2)
		}
		ok, verr := corecrypto.MACVerify("hmac-sha256", vkey, []byte(c.message), tag)
		//: MACVerify only errors on an unknown algorithm, never on a bad tag.
		if verr != nil {
			t.Fatalf("MACVerify: %v", verr)
		}
		//: the verdict must match the table expectation.
		if ok != c.wantOK {
			t.Errorf("MACVerify=%v want %v", ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestMAC_unknownAlgorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"an unregistered MAC algorithm surfaces UnknownMACAlgorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := macKey(t, 0x5)
			//: a name no package registered must surface the typed sentinel.
			_, err := corecrypto.MACTag("hmac-sha512-not-registered", key, []byte("x"))
			if !errs.HasCode(err, corecrypto.CodeUnknownMACAlgorithm) {
				t.Errorf("MACTag err=%v want UnknownMACAlgorithm", err)
			}
		})
	}
}
