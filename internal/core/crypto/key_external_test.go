package crypto_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestNewKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     []byte
		wantErr bool
	}
	tests := []tc{
		{"exactly 32 bytes is accepted", bytes.Repeat([]byte{0x7}, crypto.KeyLen), false},
		{"too short is rejected", bytes.Repeat([]byte{0x7}, crypto.KeyLen-1), true},
		{"too long is rejected", bytes.Repeat([]byte{0x7}, crypto.KeyLen+1), true},
		{"nil is rejected", nil, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		k, err := crypto.NewKey(c.raw)
		//: the failure arm must surface InvalidKey and never round-trip bytes.
		if c.wantErr {
			if !errs.HasCode(err, crypto.CodeInvalidKey) {
				t.Errorf("%s: err=%v want InvalidKey", c.name, err)
			}
			return
		}
		//: the happy arm must accept and copy the material verbatim.
		if err != nil {
			t.Fatalf("%s: unexpected err %v", c.name, err)
		}
		if !bytes.Equal(k.Bytes(), c.raw) {
			t.Errorf("%s: Bytes() did not round-trip the key", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKey_Bytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"mutating the returned slice does not affect the Key"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		raw := bytes.Repeat([]byte{0x1}, crypto.KeyLen)
		k, err := crypto.NewKey(raw)
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		out := k.Bytes()
		//: corrupt the returned copy; the Key must be unaffected.
		out[0] ^= 0xFF
		if k.Bytes()[0] != 0x1 {
			t.Errorf("Bytes() aliased the Key's backing array")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKey_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		render func(crypto.Key) string
	}
	tests := []tc{
		{"String redacts", func(k crypto.Key) string { return k.String() }},
		{"%v redacts (via Stringer)", func(k crypto.Key) string { return fmt.Sprintf("%v", k) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		k, err := crypto.NewKey(bytes.Repeat([]byte{0xAB}, crypto.KeyLen))
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		//: no rendering path may leak key bytes; all must be the marker.
		if got := c.render(k); got != "<redacted>" {
			t.Errorf("%s: got %q want <redacted>", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKey_GoString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		render func(crypto.Key) string
	}
	tests := []tc{
		{"GoString redacts", func(k crypto.Key) string { return k.GoString() }},
		{"%#v redacts (via GoStringer)", func(k crypto.Key) string { return fmt.Sprintf("%#v", k) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		k, err := crypto.NewKey(bytes.Repeat([]byte{0xAB}, crypto.KeyLen))
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		//: %#v must not dump the raw slice — GoString keeps it redacted.
		if got := c.render(k); got != "<redacted>" {
			t.Errorf("%s: got %q want <redacted>", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKey_Zeroize(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Zeroize clears the key material"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		k, err := crypto.NewKey(bytes.Repeat([]byte{0x9}, crypto.KeyLen))
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		k.Zeroize()
		//: every byte must be zero after Zeroize.
		if !bytes.Equal(k.Bytes(), make([]byte, crypto.KeyLen)) {
			t.Errorf("Zeroize did not clear the key: %x", k.Bytes())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
