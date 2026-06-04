package mac_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	mac "github.com/kitsunium/sdk/pkg/v1/mac"
)

func key32(t *testing.T, b byte) mac.Key {
	t.Helper()
	//: deterministic 32-byte key for repeatable tags.
	k, err := mac.NewKey(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

func TestMAC_tagVerify(t *testing.T) {
	t.Parallel()
	const (
		caseGenuine  int = iota // genuine tag, same key
		caseTamper              // a tag byte flipped
		caseWrongKey            // verify under a different key
	)
	type tc struct {
		name   string
		mode   int
		wantOK bool
	}
	tests := []tc{
		{"genuine tag verifies", caseGenuine, true},
		{"tampered tag fails", caseTamper, false},
		{"wrong key fails", caseWrongKey, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		k := key32(t, 0x1)
		msg := []byte("public message, secret tag")
		tag, err := mac.Tag(mac.HMACSHA256, k, msg)
		if err != nil {
			t.Fatalf("Tag: %v", err)
		}
		//: a tampered tag drives the negative verify path.
		if c.mode == caseTamper {
			tag[0] ^= 0xff
		}
		vk := k
		//: a distinct key drives the wrong-key path.
		if c.mode == caseWrongKey {
			vk = key32(t, 0x2)
		}
		ok, verr := mac.Verify(mac.HMACSHA256, vk, msg, tag)
		//: Verify only errors on an unknown algorithm, never on a bad tag.
		if verr != nil {
			t.Fatalf("Verify: %v", verr)
		}
		if ok != c.wantOK {
			t.Errorf("Verify=%v want %v", ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAlgorithmIsDomainDefinedType_V104 pins the V104 fix: mac.Algorithm is a
// DEFINED type owned by this facade, not a bare alias of the shared
// internal/core/crypto.Algorithm. While Algorithm was an alias, all seven
// crypto-family facades shared one identical Go type, so a hash or signature
// constant fed into a MAC call type-checked and only misrouted at runtime. A
// defined type makes that cross-domain mix a compile error; reflection witnesses
// the change because a defined type reports its own package path, whereas an
// alias reports internal/core/crypto. This test FAILS before the alias→defined-
// type change and PASSES after.
func TestAlgorithmIsDomainDefinedType_V104(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	rt := reflect.TypeFor[mac.Algorithm]()
	tests := []tc{
		//: a defined type reports its declaring package; an alias reports corecrypto's.
		{"package path is this facade", rt.PkgPath(), "github.com/kitsunium/sdk/pkg/v1/mac"},
		//: the defined type names itself Algorithm in this package.
		{"type name is Algorithm", rt.Name(), "Algorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a mismatch means Algorithm is still an alias of corecrypto.Algorithm.
			if c.got != c.want {
				t.Errorf("mac.Algorithm %s=%q want %q (still an alias?)", c.name, c.got, c.want)
			}
		})
	}
}

// TestKeyLenAndNewKeyExposed_V105 pins the V105 fix: every Key-aliasing facade
// re-exports BOTH NewKey and KeyLen uniformly. Before the fix mac exposed NewKey
// but not KeyLen, so the mac.go doc reference to "KeyLen (32) bytes" pointed at
// an identifier absent from package mac. This test references mac.KeyLen and
// round-trips mac.NewKey, so it does not compile before the const is added and
// PASSES after. The wrong-length rejection routes through the errs API, never
// .Error() string matching.
func TestKeyLenAndNewKeyExposed_V105(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     []byte
		wantErr bool
	}
	tests := []tc{
		{"exactly KeyLen bytes round-trips", bytes.Repeat([]byte{0x1}, mac.KeyLen), false},
		{"a short key is rejected with INVALID_KEY", bytes.Repeat([]byte{0x1}, mac.KeyLen-1), true},
	}
	//: KeyLen is the frozen 256-bit symmetric key length the mac.go doc references.
	if mac.KeyLen != 32 {
		t.Fatalf("mac.KeyLen=%d want 32", mac.KeyLen)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := mac.NewKey(c.raw)
			//: a wrong-length key surfaces the typed INVALID_KEY reason, nothing else.
			if c.wantErr {
				if !errs.HasReason(err, "INVALID_KEY") {
					t.Errorf("%s: NewKey reason=%v want INVALID_KEY", c.name, err)
				}
				return
			}
			//: an exactly-KeyLen slice builds a redacting Key without error.
			if err != nil {
				t.Errorf("%s: NewKey: %v", c.name, err)
			}
		})
	}
}
