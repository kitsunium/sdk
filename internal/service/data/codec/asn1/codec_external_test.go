package asn1_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreasn1 "github.com/kitsunium/sdk/internal/core/data/codec/asn1"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/asn1"
)

type asn1Payload struct {
	N int    `json:"n"`
	S string `json:"s"`
}

// TestMarshal covers the asn1 codec's Marshal path including the
// MARSHAL_FAILED error case on unsupported types.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"struct payload", asn1Payload{N: 42, S: "x"}, ""},
		{"channel triggers MARSHAL_FAILED", make(chan int), "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := asn1.New().Marshal(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal covers the asn1 codec's Unmarshal path including the
// UNMARSHAL_FAILED branch on malformed bytes.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	good, gerr := asn1.New().Marshal(asn1Payload{N: 1, S: "a"})
	if gerr != nil {
		t.Fatalf("seed Marshal err=%v", gerr)
	}
	tests := []tc{
		{"round-trip success", good, false},
		{"malformed bytes surface UNMARSHAL_FAILED", []byte{0xff, 0x00}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out asn1Payload
		err := asn1.New().Unmarshal(tc.data, &out)
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr && !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("%s: expected UNMARSHAL_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNew verifies the public constructor returns a non-nil singleton with
// the canonical name advertised.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "asn1-der"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := asn1.New()
		if c == nil {
			t.Fatalf("%s: New() returned nil", tc.name)
		}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport asserts the codec self-registers under its Name,
// MIME, and primary extension when the package is imported.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool {
			_, ok := codec.Lookup(codec.Format("asn1-der"))
			return ok
		}},
		{"MIME resolved", func() bool {
			_, ok := codec.LookupMIME("application/pkix-cert")
			return ok
		}},
		{"extension resolved", func() bool {
			_, ok := codec.LookupExt(".der")
			return ok
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewStrictRefusesTrailingData is #904: encoding/asn1.Unmarshal returns
// the bytes past the first structure and the registered codec drops them, so
// a signed blob with a byte appended decodes equal to the original. The strict
// instance refuses them with TRAILING_DATA; the registered one keeps its
// lenient contract; both read the exact encoding.
func TestNewStrictRefusesTrailingData(t *testing.T) {
	t.Parallel()
	der, err := asn1.New().Marshal(asn1Payload{N: 42, S: "x"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	strict := asn1.NewStrict()
	if registered, _ := codec.Lookup("asn1-der"); registered == strict {
		t.Fatal("the strict instance must not be the registered codec")
	}
	var got asn1Payload
	if err := strict.Unmarshal(der, &got); err != nil || got != (asn1Payload{N: 42, S: "x"}) {
		t.Fatalf("strict Unmarshal of the exact DER: %+v, %v", got, err)
	}
	trailing := append(append([]byte{}, der...), 0x00)
	serr := strict.Unmarshal(trailing, &got)
	if !errs.HasReason(serr, "TRAILING_DATA") || !errs.HasCode(serr, coreasn1.CodeASN1TrailingData) {
		t.Fatalf("strict Unmarshal of DER||00: want TRAILING_DATA (0.3.9.3), got %v", serr)
	}
	if err := asn1.New().Unmarshal(trailing, &got); err != nil {
		t.Fatalf("the registered codec must keep accepting trailing bytes, got %v", err)
	}
	if out, err := strict.Marshal(asn1Payload{N: 42, S: "x"}); err != nil || string(out) != string(der) {
		t.Fatalf("strict Marshal differs from the registered one: %x, %v", out, err)
	}
}

// TestUnmarshalReadsTheBERFormsTheDocNames pins the design's statement of
// which BER forms encoding/asn1 reads: each row is decoded by both the
// registered and the strict instance, which agree on every one of them.
func TestUnmarshalReadsTheBERFormsTheDocNames(t *testing.T) {
	t.Parallel()
	type seq struct{ A int }
	type withDefault struct {
		A int `asn1:"default:5"`
		B int
	}
	type setOf struct {
		S []int `asn1:"set"`
	}
	tests := []struct {
		name     string
		data     []byte
		target   func() any
		accepted bool
	}{
		{"an indefinite-length SEQUENCE", []byte{0x30, 0x80, 0x02, 0x01, 0x05, 0x00, 0x00}, func() any { return new(seq) }, false},
		{"a length not in its shortest form", []byte{0x30, 0x81, 0x03, 0x02, 0x01, 0x05}, func() any { return new(seq) }, false},
		{"a tag number not in its shortest form", []byte{0x1f, 0x05, 0x01, 0x00}, func() any { return new(int) }, false},
		{"a non-minimal INTEGER", []byte{0x02, 0x02, 0x00, 0x05}, func() any { return new(int) }, false},
		{"a BOOLEAN of 0x01", []byte{0x01, 0x01, 0x01}, func() any { return new(bool) }, false},
		{"a constructed OCTET STRING", []byte{0x24, 0x06, 0x04, 0x01, 0xaa, 0x04, 0x01, 0xbb}, func() any { return new([]byte) }, false},
		{"a UTCTime without seconds", []byte("\x17\x0b0601021504Z"), func() any { return new(time.Time) }, true},
		{"a UTCTime with an offset", []byte("\x17\x11060102150405+0100"), func() any { return new(time.Time) }, true},
		{"a DEFAULT value written out", []byte{0x30, 0x06, 0x02, 0x01, 0x05, 0x02, 0x01, 0x07}, func() any { return new(withDefault) }, true},
		{"a SET OF out of order", []byte{0x30, 0x08, 0x31, 0x06, 0x02, 0x01, 0x07, 0x02, 0x01, 0x05}, func() any { return new(setOf) }, true},
		{"a PrintableString carrying '*'", []byte{0x13, 0x01, '*'}, func() any { return new(string) }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range []codec.Codec{asn1.New(), asn1.NewStrict()} {
				err := c.Unmarshal(tc.data, tc.target())
				if tc.accepted && err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				if !tc.accepted && !errs.HasReason(err, "UNMARSHAL_FAILED") {
					t.Fatalf("want UNMARSHAL_FAILED, got %v", err)
				}
			}
		})
	}
}
