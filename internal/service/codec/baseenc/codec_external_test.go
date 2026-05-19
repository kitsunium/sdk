package baseenc_test

import (
	"bytes"
	"encoding/ascii85"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/baseenc"
)

// payload is the canonical struct used by every round-trip subtest.
type payload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// codecTC is the table-row used by the format-keyed round-trip suites.
type codecTC struct {
	format string
	mime   string
	ext    string
}

// malformedTC is the table-row for decoder rejection suites.
type malformedTC struct {
	format string
	bad    []byte
}

// envTC is the table-row for malformed-JSON-inside-base-N envelope suites.
type envTC struct {
	format string
	env    []byte
}

// singletonTC is the table-row for the package-level singleton assertion.
type singletonTC struct {
	name string
	c    codec.Codec
	want string
}

// codecs enumerates each registered codec by Format string. Tests iterate
// over this list so a new variant only needs an entry here.
var codecs = []codecTC{
	{"base64", "application/base64", ".b64"},
	{"base64url", "application/base64url", ".b64url"},
	{"base32", "application/base32", ".b32"},
	{"base16", "application/base16", ".b16"},
	{"hex", "application/hex", ".hex"},
	{"ascii85", "application/ascii85", ".a85"},
}

// runRoundTrip drives one Marshal/Unmarshal round-trip for a registered
// codec. Factored into a named function so the loop in
// TestMarshalUnmarshalRoundTrip does not capture tc via a closure literal —
// passing tc by value keeps the heap escape away from the inner subtest.
func runRoundTrip(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, ok := codec.Lookup(codec.Format(tc.format))
		if !ok {
			t.Fatalf("Lookup %q missed", tc.format)
		}
		data, merr := c.Marshal(payload{Name: "a", Age: 1})
		if merr != nil {
			t.Fatalf("Marshal err=%v", merr)
		}
		var got payload
		if uerr := c.Unmarshal(data, &got); uerr != nil {
			t.Fatalf("Unmarshal err=%v", uerr)
		}
		if got.Name != "a" || got.Age != 1 {
			t.Errorf("round-trip mismatch got=%+v", got)
		}
	}
}

// TestMarshalUnmarshalRoundTrip verifies that every registered codec can
// encode a struct and decode it back without loss.
func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runRoundTrip(tc))
	}
}

// runRegisteredViaImport asserts the Format / MIME / Extension entries are
// present in the registry for a given variant. Passed by value so the
// inner subtest does not capture tc via closure.
func runRegisteredViaImport(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		if _, ok := codec.Lookup(codec.Format(tc.format)); !ok {
			t.Errorf("Format %q not registered", tc.format)
		}
		if _, ok := codec.LookupMIME(tc.mime); !ok {
			t.Errorf("MIME %q not registered for %q", tc.mime, tc.format)
		}
		if _, ok := codec.LookupExt(tc.ext); !ok {
			t.Errorf("Extension %q not registered for %q", tc.ext, tc.format)
		}
	}
}

// TestRegisteredViaImport verifies each variant self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runRegisteredViaImport(tc))
	}
}

// runMarshalRejectsUnsupported asserts Marshal surfaces the dedicated
// BASE_ENC_MARSHAL_FAILED reason when JSON cannot serialise the value.
func runMarshalRejectsUnsupported(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		_, err := c.Marshal(make(chan int))
		if !errs.HasReason(err, "BASE_ENC_MARSHAL_FAILED") {
			t.Errorf("expected BASE_ENC_MARSHAL_FAILED, got %v", err)
		}
	}
}

// TestMarshalRejectsUnsupportedValue surfaces BASE_ENC_MARSHAL_FAILED when
// encoding/json rejects the supplied value (channel, function, complex).
func TestMarshalRejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runMarshalRejectsUnsupported(tc))
	}
}

// runUnmarshalRejectsMalformed asserts the stdlib decoder failure surfaces
// the typed BASE_ENC_DECODE_FAILED sentinel for the variant.
func runUnmarshalRejectsMalformed(tc malformedTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		var out payload
		err := c.Unmarshal(tc.bad, &out)
		if !errs.HasReason(err, "BASE_ENC_DECODE_FAILED") {
			t.Errorf("expected BASE_ENC_DECODE_FAILED, got %v", err)
		}
	}
}

// TestUnmarshalRejectsMalformedBase surfaces BASE_ENC_DECODE_FAILED when
// the base-N decoder rejects the input bytes.
func TestUnmarshalRejectsMalformedBase(t *testing.T) {
	t.Parallel()
	tests := []malformedTC{
		{"base64", []byte("!!!notb64!!!")},
		{"base64url", []byte("!!!notb64!!!")},
		{"base32", []byte("!!!notb32!!!")},
		{"base16", []byte("ZZZZ")},
		{"hex", []byte("ZZZZ")},
		{"ascii85", []byte{0x00, 0x01, 0xFF}},
	}
	for _, tc := range tests {
		t.Run(tc.format, runUnmarshalRejectsMalformed(tc))
	}
}

// runUnmarshalRejectsBadJSON asserts the recovered JSON-layer failure
// surfaces the typed BASE_ENC_UNMARSHAL_FAILED sentinel.
func runUnmarshalRejectsBadJSON(tc envTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		var out payload
		err := c.Unmarshal(tc.env, &out)
		if !errs.HasReason(err, "BASE_ENC_UNMARSHAL_FAILED") {
			t.Errorf("expected BASE_ENC_UNMARSHAL_FAILED, got %v (env=%q)", err, tc.env)
		}
	}
}

// TestUnmarshalRejectsBadJSON surfaces BASE_ENC_UNMARSHAL_FAILED when the
// recovered JSON layer is malformed. The envelope is built by direct
// stdlib base-N encoding of non-JSON bytes — the base-N step succeeds
// but the inner json.Unmarshal rejects "{not-json".
func TestUnmarshalRejectsBadJSON(t *testing.T) {
	t.Parallel()
	tests := []envTC{
		{"base64", encodeForTest("base64", []byte("{not-json"))},
		{"base64url", encodeForTest("base64url", []byte("{not-json"))},
		{"base32", encodeForTest("base32", []byte("{not-json"))},
		{"base16", encodeForTest("base16", []byte("{not-json"))},
		{"hex", encodeForTest("hex", []byte("{not-json"))},
		{"ascii85", encodeForTest("ascii85", []byte("{not-json"))},
	}
	for _, tc := range tests {
		t.Run(tc.format, runUnmarshalRejectsBadJSON(tc))
	}
}

// runUnmarshalRejectsOversize asserts the 10 MiB cap fires for each variant.
func runUnmarshalRejectsOversize(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		big := make([]byte, 10*1024*1024+1)
		var out payload
		err := c.Unmarshal(big, &out)
		if !errs.HasReason(err, "BASE_ENC_SIZE_EXCEEDED") {
			t.Errorf("expected BASE_ENC_SIZE_EXCEEDED, got %v", err)
		}
	}
}

// TestUnmarshalRejectsOversize surfaces BASE_ENC_SIZE_EXCEEDED when the
// input exceeds the 10 MiB cap.
func TestUnmarshalRejectsOversize(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runUnmarshalRejectsOversize(tc))
	}
}

// runAppendRoundTrip drives one Append round-trip and cross-checks the tail
// against Marshal so the Appender contract is preserved per variant.
func runAppendRoundTrip(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		a, ok := c.(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.format)
		}
		prefix := []byte("prefix:")
		out, err := a.Append(slices.Clone(prefix), payload{Name: "a", Age: 1})
		if err != nil {
			t.Fatalf("Append err=%v", err)
		}
		if !bytes.HasPrefix(out, prefix) {
			t.Errorf("Append dropped prefix; got=%q", out)
		}
		marshalled, merr := c.Marshal(payload{Name: "a", Age: 1})
		if merr != nil {
			t.Fatalf("Marshal err=%v", merr)
		}
		if !bytes.Equal(out[len(prefix):], marshalled) {
			t.Errorf("Append tail differs from Marshal: got=%q want=%q",
				out[len(prefix):], marshalled)
		}
	}
}

// TestAppendRoundTrip verifies the Appender extension yields the same bytes
// as Marshal and preserves an existing prefix on dst.
func TestAppendRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runAppendRoundTrip(tc))
	}
}

// runAppendRejectsUnsupported asserts the Appender contract rolls dst back
// to its original length on a JSON-side failure.
func runAppendRejectsUnsupported(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		a := c.(codec.Appender)
		prefix := []byte("prefix:")
		out, err := a.Append(slices.Clone(prefix), make(chan int))
		if !errs.HasReason(err, "BASE_ENC_MARSHAL_FAILED") {
			t.Errorf("expected BASE_ENC_MARSHAL_FAILED, got %v", err)
		}
		if !bytes.Equal(out, prefix) {
			t.Errorf("Append should leave dst untouched on error, got=%q", out)
		}
	}
}

// TestAppendRejectsUnsupportedValue surfaces BASE_ENC_MARSHAL_FAILED and
// leaves dst untouched on JSON-side failure.
func TestAppendRejectsUnsupportedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runAppendRejectsUnsupported(tc))
	}
}

// runStreamingRoundTrip drives the NewEncoder/NewDecoder pair for one
// variant. All registered baseenc variants implement codec.StreamingCodec,
// so a missing implementation is a contract regression — assert it as a
// hard failure rather than skipping.
func runStreamingRoundTrip(tc codecTC) func(t *testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		c, _ := codec.Lookup(codec.Format(tc.format))
		sc, ok := c.(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.format)
		}
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		if err := enc.Encode(payload{Name: "a", Age: 1}); err != nil {
			t.Fatalf("Encode err=%v", err)
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("Close err=%v", err)
		}
		if buf.Len() == 0 {
			t.Fatalf("streamed output empty")
		}
		dec := sc.NewDecoder(bytes.NewReader(buf.Bytes()))
		var got payload
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("Decode err=%v", err)
		}
		if got.Name != "a" || got.Age != 1 {
			t.Errorf("streaming round-trip mismatch got=%+v", got)
		}
	}
}

// TestStreamingRoundTrip exercises NewEncoder/NewDecoder for every variant.
func TestStreamingRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range codecs {
		t.Run(tc.format, runStreamingRoundTrip(tc))
	}
}

// TestSingletonsExported verifies the package-level singleton vars are
// non-nil and resolve to their expected Format.
func TestSingletonsExported(t *testing.T) {
	t.Parallel()
	tests := []singletonTC{
		{"Base64", baseenc.Base64, "base64"},
		{"Base64URL", baseenc.Base64URL, "base64url"},
		{"Base32", baseenc.Base32, "base32"},
		{"Base16", baseenc.Base16, "base16"},
		{"Hex", baseenc.Hex, "hex"},
		{"ASCII85", baseenc.ASCII85, "ascii85"},
	}
	runCase := func(t *testing.T, tc singletonTC) {
		t.Helper()
		if tc.c == nil {
			t.Fatalf("%s: singleton is nil", tc.name)
		}
		if got := tc.c.Name(); got != tc.want {
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

// encodeForTest base-N encodes raw bytes directly via stdlib helpers,
// bypassing the JSON wrap step. Used by TestUnmarshalRejectsBadJSON to
// plant non-JSON content inside the base-N envelope.
func encodeForTest(format string, raw []byte) []byte {
	switch format {
	case "base64":
		out := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
		base64.StdEncoding.Encode(out, raw)
		return out
	case "base64url":
		out := make([]byte, base64.URLEncoding.EncodedLen(len(raw)))
		base64.URLEncoding.Encode(out, raw)
		return out
	case "base32":
		out := make([]byte, base32.StdEncoding.EncodedLen(len(raw)))
		base32.StdEncoding.Encode(out, raw)
		return out
	case "base16":
		out := make([]byte, hex.EncodedLen(len(raw)))
		hex.Encode(out, raw)
		return bytes.ToUpper(out)
	case "hex":
		out := make([]byte, hex.EncodedLen(len(raw)))
		hex.Encode(out, raw)
		return out
	case "ascii85":
		buf := make([]byte, ascii85.MaxEncodedLen(len(raw)))
		n := ascii85.Encode(buf, raw)
		return buf[:n]
	default:
		//: unknown formats are a programmer error in the test table —
		//: return nil so callers see an empty envelope and the downstream
		//: assertion (which expects a typed sentinel) fails loudly.
		return nil
	}
}
