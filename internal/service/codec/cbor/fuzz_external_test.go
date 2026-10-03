package cbor_test

import (
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
)

// fuzzSeeds are RFC 8949 Appendix A, the encodings the golden tests pin, and
// the malformed shapes the validator refuses.
var fuzzSeeds = []string{
	"00", "17", "1818", "1903e8", "1bffffffffffffffff", "20", "3bffffffffffffffff",
	"c249010000000000000000", "c349010000000000000000",
	"f90000", "f98000", "f97bff", "fa47c35000", "fb3ff199999999999a", "f97c00", "f97e00", "f9fc00",
	"f4", "f5", "f6", "f7", "f0", "f8ff",
	"c074323031332d30332d32315432303a30343a30305a", "c11a514b67b0", "c1fb41d452d9ec200000",
	"d74401020304", "d82076687474703a2f2f7777772e6578616d706c652e636f6d", "d9d9f71863",
	"40", "4401020304", "60", "6449455446", "62c3bc", "64f0908591",
	"80", "8301820203820405", "98190102030405060708090a0b0c0d0e0f101112131415161718181819",
	"a0", "a201020304", "a26161016162820203", "826161a161626163",
	"5f42010243030405ff", "7f657374726561646d696e67ff", "9fff", "9f018202039f0405ffff",
	"bf61610161629f0203ffff", "bf6346756ef563416d7421ff",
	"a3646e616d656341646163616765182465456d61696c6161", "830161784109", "a30101216178616303",
	"1c", "1f", "ff", "f818", "5f6161ff", "bf01ff", "62fffe", "c001", "830102", "9f0102",
}

// fuzzTarget is a struct of every kind of field, for the typed leg.
type fuzzTarget struct {
	A int               `cbor:"a"`
	B string            `cbor:"b"`
	C []byte            `cbor:"c"`
	D float32           `cbor:"d"`
	E map[string]int    `cbor:"e"`
	F [2]uint8          `cbor:"f"`
	G *fuzzTarget       `cbor:"g"`
	H time.Time         `cbor:"h"`
	I []any             `cbor:"i"`
	J bool              `cbor:"j"`
	K int8              `cbor:"1,keyasint"`
	L uint16            `cbor:"-1,keyasint"`
	M any               `cbor:"m"`
	N map[int]string    `cbor:"n"`
	O map[any]any       `cbor:"o"`
	P toArray           `cbor:"p"`
	Q map[string]string `cbor:"q"`
	R *int              `cbor:"r"`
}

// FuzzUnmarshal drives arbitrary bytes through every decoding path.
//
// Invariants beyond "it does not panic", which the runtime enforces:
//
//  1. CONVERGENCE — whatever decodes untyped re-encodes (unless two of its
//     keys encode alike, which a decoded time or bignum can produce, and
//     which the encoder refuses by name), decodes again, and from there
//     encodes to the same bytes: the round trip reaches a fixed point after
//     one pass, since a time or a bignum comes back as a plain integer.
//  2. STREAM EQUIVALENCE — the stream decoder accepts exactly one item where
//     Unmarshal does, then reports io.EOF.
//  3. TYPED SAFETY — decoding into a struct of every kind of field succeeds
//     or fails with UNMARSHAL_FAILED; nothing else comes out.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range fuzzSeeds {
		data, err := hex.DecodeString(seed)
		if err != nil {
			f.Fatalf("bad seed %q: %v", seed, err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var untyped any
		err := cbor.New().Unmarshal(data, &untyped)
		if err != nil && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("Unmarshal(%x) = %v, not UNMARSHAL_FAILED", data, err)
		}
		fuzzAssertStream(t, data, err == nil)
		fuzzAssertTyped(t, data)
		if err == nil {
			fuzzAssertConverges(t, data, untyped)
		}
	})
}

// fuzzAssertConverges re-encodes an untyped result and checks the fixed point.
func fuzzAssertConverges(t *testing.T, data []byte, untyped any) {
	t.Helper()
	first, err := cbor.New().Marshal(untyped)
	if err != nil {
		if strings.Contains(errs.PrivateOf(err), "same CBOR key") {
			return
		}
		t.Fatalf("re-encoding what %x decoded to failed: %v (%s)", data, err, errs.PrivateOf(err))
	}
	var again any
	if uerr := cbor.New().Unmarshal(first, &again); uerr != nil {
		t.Fatalf("decoding the codec's own %x failed: %v (%s)", first, uerr, errs.PrivateOf(uerr))
	}
	second, err := cbor.New().Marshal(again)
	if err != nil {
		if strings.Contains(errs.PrivateOf(err), "same CBOR key") {
			return
		}
		t.Fatalf("second re-encoding of %x failed: %v", data, err)
	}
	var third any
	if uerr := cbor.New().Unmarshal(second, &third); uerr != nil {
		t.Fatalf("decoding %x failed: %v", second, uerr)
	}
	fixed, err := cbor.New().Marshal(third)
	if err != nil || !bytes.Equal(fixed, second) {
		t.Fatalf("no fixed point for %x: %x then %x (%v)", data, second, fixed, err)
	}
}

// fuzzAssertStream checks the stream decoder agrees with Unmarshal.
func fuzzAssertStream(t *testing.T, data []byte, single bool) {
	t.Helper()
	dec := streaming(t).NewDecoder(bytes.NewReader(data))
	var first any
	err := dec.Decode(&first)
	if !single {
		return
	}
	if err != nil {
		t.Fatalf("Unmarshal accepted %x but the stream decoder refused it: %v", data, err)
	}
	var second any
	if end := dec.Decode(&second); end != io.EOF {
		t.Fatalf("after the single item of %x the stream decoder returned %v, want io.EOF", data, end)
	}
}

// fuzzAssertTyped decodes into the struct target.
func fuzzAssertTyped(t *testing.T, data []byte) {
	t.Helper()
	var target fuzzTarget
	if err := cbor.New().Unmarshal(data, &target); err != nil && !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("typed Unmarshal(%x) = %v, not UNMARSHAL_FAILED", data, err)
	}
}
