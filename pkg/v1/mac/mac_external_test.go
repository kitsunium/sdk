package mac_test

import (
	"bytes"
	"testing"

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
