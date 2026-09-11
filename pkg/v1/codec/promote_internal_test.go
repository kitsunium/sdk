package codec

import (
	"encoding/binary"
	stdjson "encoding/json"
	stdpem "encoding/pem"
	"errors"
	"net/url"
	"strings"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	fbcodec "github.com/kitsunium/sdk/internal/service/codec/flatbuffers"
)

// lookupCodec resolves a registered codec from the core registry for the
// promotion-path tests that need a real codec to drive the inner
// Marshal / Unmarshal failure arms (the blank imports in codec.go have
// already registered every Format by the time the test runs).
func lookupCodec(t *testing.T, f Format) corecodec.Codec {
	t.Helper()
	//: registry lookup mirrors what the public facade does.
	c, ok := corecodec.Lookup(f)
	//: a missing registration is a build-wiring bug, not a test input.
	if !ok {
		t.Fatalf("codec %q not registered", f)
	}
	//: caller drives the promotion path with the concrete codec.
	return c
}

// promoteCanaryUser is the canonical fixture every promote-side test
// runs against — small enough that all binary codecs fit the JSON
// representation in a single record.
type promoteCanaryUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// canaryJSONBytes returns the json encoding of the canary fixture so
// individual wrap/extract tests don't have to re-marshal it.
func canaryJSONBytes(t *testing.T) []byte {
	t.Helper()
	//: stdjson is the bridge format every promotion strategy embeds.
	out, err := stdjson.Marshal(promoteCanaryUser{Name: "Ada", Age: 36})
	//: this can only fail on a programming error in the fixture.
	if err != nil {
		t.Fatalf("canary fixture json.Marshal err=%v", err)
	}
	//: caller drives the wrap / unwrap with the bytes.
	return out
}

// TestPromoteFailedFor asserts the typed PromoteFailed sentinel is
// emitted with the f- and detail-specific Private message.
func TestPromoteFailedFor(t *testing.T) {
	t.Parallel()
	//: case table — Format + detail string + Private fragment we
	//: expect to find inside the typed wrap.
	type tc struct {
		name    string
		format  Format
		detail  string
		private string
	}
	tests := []tc{
		{name: "unknown-marshal", format: Format("never-registered"), detail: "no marshal strategy", private: "never-registered"},
		{name: "unknown-unmarshal", format: Format("phantom"), detail: "no unmarshal strategy", private: "phantom"},
	}
	//: per-case driver — assertion is reason + private fragment.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := promoteFailedFor(tc.format, tc.detail)
		//: typed sentinel must carry PROMOTE_FAILED.
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Fatalf("%s: reason=%q ok=%v want PROMOTE_FAILED", tc.name, reason, ok)
		}
		//: Private must mention the detail + format token.
		if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.detail) || !strings.Contains(priv, tc.private) {
			t.Errorf("%s: Private=%q missing detail=%q or format=%q", tc.name, priv, tc.detail, tc.private)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteContainerFailed mirrors TestPromoteFailedFor but for the
// container-side sentinel raised inside extract closures.
func TestPromoteContainerFailed(t *testing.T) {
	t.Parallel()
	//: every constrained codec emits its own detail string, so the
	//: table covers the 5 known shapes.
	type tc struct {
		name   string
		detail string
	}
	tests := []tc{
		{name: "ndjson", detail: "empty ndjson promotion container"},
		{name: "csv", detail: "malformed csv promotion container"},
		{name: "pem", detail: "nil pem promotion block"},
		{name: "flatbuffers", detail: "flatbuffers promotion payload too short"},
		{name: "tlv", detail: "tlv promotion mismatch"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := promoteContainerFailed(tc.detail)
		//: typed sentinel must carry PROMOTE_FAILED.
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Fatalf("%s: reason=%q want PROMOTE_FAILED", tc.name, reason)
		}
		//: detail must appear in Private.
		if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.detail) {
			t.Errorf("%s: Private=%q missing detail=%q", tc.name, priv, tc.detail)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestIsValueShapeMismatch asserts the reason-based detection that
// gates the promotion retry: every constrained codec's shape-rejection
// reason returns true, everything else returns false.
func TestIsValueShapeMismatch(t *testing.T) {
	t.Parallel()
	//: build a typed error from a reason so the helper has something
	//: to walk.
	mkErr := func(reason string) error {
		//: typed wrap with the given reason; code value is irrelevant
		//: because the detector only looks at the reason string.
		return kerrs.Wrap(nil, kerrs.WrapParams{
			Code:    CodePromoteFailed,
			Reason:  reason,
			Public:  "test",
			Private: "test",
		})
	}
	type tc struct {
		name   string
		err    error
		expect bool
	}
	tests := []tc{
		{name: "nil-error", err: nil, expect: false},
		{name: "untyped-error", err: errors.New("plain"), expect: false},
		{name: "value-invalid", err: mkErr("VALUE_INVALID"), expect: true},
		{name: "flatbuffers-bad-type", err: mkErr("FLATBUFFERS_BAD_TYPE"), expect: true},
		{name: "flatbuffers-bad-target", err: mkErr("FLATBUFFERS_BAD_TARGET"), expect: true},
		{name: "unmarshal-failed", err: mkErr("UNMARSHAL_FAILED"), expect: true},
		{name: "unrelated", err: mkErr("UNKNOWN_FORMAT"), expect: false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := isValueShapeMismatch(tc.err)
		//: bool assertion.
		if got != tc.expect {
			t.Errorf("%s: got=%v want=%v err=%v", tc.name, got, tc.expect, tc.err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestWrapForFormat asserts every promotion-supported Format produces
// a container that, when re-marshaled by stdjson, recovers the inner
// bytes byte-for-byte (proof that the wrap step is lossless).
func TestWrapForFormat(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	//: cache the bytes→string conversion once for every closure
	//: comparison (KTN-VAR-BYTESCONV).
	innerStr := string(inner)
	type tc struct {
		name   string
		format Format
		check  func(t *testing.T, container any)
	}
	tests := []tc{
		{
			name: "ndjson", format: NDJSON,
			check: func(t *testing.T, container any) {
				rows, ok := container.([]stdjson.RawMessage)
				if !ok || len(rows) != 1 || string(rows[0]) != innerStr {
					t.Errorf("ndjson wrap shape=%T value=%v", container, container)
				}
			},
		},
		{
			name: "csv", format: CSV,
			check: func(t *testing.T, container any) {
				rows, ok := container.([][]string)
				if !ok || len(rows) != csvPromotionRowCount || rows[0][0] != csvPromotionHeader || rows[1][0] != innerStr {
					t.Errorf("csv wrap shape=%T value=%v", container, container)
				}
			},
		},
		{
			name: "pem", format: PEM,
			check: func(t *testing.T, container any) {
				block, ok := container.(*stdpem.Block)
				if !ok || block == nil || block.Type != pemPromotionBlockType || string(block.Bytes) != innerStr {
					t.Errorf("pem wrap shape=%T value=%v", container, container)
				}
			},
		},
		{
			name: "flatbuffers", format: Format("flatbuffers"),
			check: func(t *testing.T, container any) {
				raw, ok := container.([]byte)
				if !ok || len(raw) < flatBuffersHeaderBytes {
					t.Errorf("flatbuffers wrap shape=%T value=%v", container, container)
					return
				}
				//: 4-byte header is fbcodec.PromotionMagic LE — lets the
				//: flatbuffers codec recognise our own wrapper bytes and
				//: skip its validateBuffer step on the recursive Marshal.
				if binary.LittleEndian.Uint32(raw[:flatBuffersHeaderBytes]) != fbcodec.PromotionMagic {
					t.Errorf("flatbuffers wrap header want=PromotionMagic got=% x", raw[:flatBuffersHeaderBytes])
				}
				//: payload after header must equal inner verbatim.
				if string(raw[flatBuffersHeaderBytes:]) != innerStr {
					t.Errorf("flatbuffers wrap payload mismatch")
				}
			},
		},
		{
			name: "tlv", format: Format("tlv"),
			check: func(t *testing.T, container any) {
				s, ok := container.(string)
				if !ok || s != innerStr {
					t.Errorf("tlv wrap shape=%T value=%v", container, container)
				}
			},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		container, err := wrapForFormat(tc.format, inner)
		if err != nil {
			t.Fatalf("%s: wrap err=%v", tc.name, err)
		}
		tc.check(t, container)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestWrapForFormat_Unknown asserts an unknown format surfaces the
// typed PromoteFailed sentinel.
func TestWrapForFormat_Unknown(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	type tc struct {
		name   string
		format Format
	}
	tests := []tc{
		{name: "phantom", format: Format("phantom")},
		{name: "empty", format: Format("")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := wrapForFormat(tc.format, inner)
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Errorf("%s: reason=%q want PROMOTE_FAILED (err=%v)", tc.name, reason, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestContainerForFormat asserts the (containerPtr, extract) pair
// roundtrips a wrap'd payload byte-for-byte through the JSON bridge.
// containerForFormat is exercised end-to-end via wrapForFormat output.
func TestContainerForFormat(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	//: cache the bytes→string conversion once for the seed closures
	//: that materialise csv + tlv payloads (KTN-VAR-BYTESCONV).
	innerStr := string(inner)
	//: drive each format through its container shape using only the
	//: pointer/extract pair; bypass the codec entirely so the test
	//: stays focused on the promotion plumbing.
	type tc struct {
		name   string
		format Format
		seed   func(t *testing.T, ptr any)
	}
	tests := []tc{
		{
			name: "ndjson", format: NDJSON,
			seed: func(t *testing.T, ptr any) {
				rows := ptr.(*[]stdjson.RawMessage)
				*rows = []stdjson.RawMessage{inner}
			},
		},
		{
			name: "csv", format: CSV,
			seed: func(t *testing.T, ptr any) {
				table := ptr.(*[][]string)
				*table = [][]string{{csvPromotionHeader}, {innerStr}}
			},
		},
		{
			name: "pem", format: PEM,
			seed: func(t *testing.T, ptr any) {
				block := ptr.(**stdpem.Block)
				*block = &stdpem.Block{Type: pemPromotionBlockType, Bytes: inner}
			},
		},
		{
			name: "flatbuffers", format: Format("flatbuffers"),
			seed: func(t *testing.T, ptr any) {
				raw := ptr.(*[]byte)
				out := make([]byte, flatBuffersHeaderBytes+len(inner))
				binary.LittleEndian.PutUint32(out[:flatBuffersHeaderBytes], 0)
				copy(out[flatBuffersHeaderBytes:], inner)
				*raw = out
			},
		},
		{
			name: "tlv", format: Format("tlv"),
			seed: func(t *testing.T, ptr any) {
				s := ptr.(*string)
				*s = innerStr
			},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		ptr, extract, err := containerForFormat(tc.format)
		if err != nil {
			t.Fatalf("%s: containerForFormat err=%v", tc.name, err)
		}
		//: seed the container as the codec would have done.
		tc.seed(t, ptr)
		got, eerr := extract()
		if eerr != nil {
			t.Fatalf("%s: extract err=%v", tc.name, eerr)
		}
		if string(got) != innerStr {
			t.Errorf("%s: extract=%q want=%q", tc.name, got, inner)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestContainerForFormat_Unknown asserts an unknown format surfaces
// the typed PromoteFailed sentinel.
func TestContainerForFormat_Unknown(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format Format
	}
	tests := []tc{
		{name: "phantom", format: Format("phantom")},
		{name: "empty", format: Format("")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, _, err := containerForFormat(tc.format)
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Errorf("%s: reason=%q want PROMOTE_FAILED (err=%v)", tc.name, reason, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractNDJSON asserts the ndjson extract closure returns the
// first row on success and the typed sentinel on empty input.
func TestExtractNDJSON(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	type tc struct {
		name       string
		rows       []stdjson.RawMessage
		want       []byte
		wantErr    bool
		wantInPriv string
	}
	tests := []tc{
		{name: "single-row", rows: []stdjson.RawMessage{inner}, want: inner, wantErr: false},
		{name: "empty", rows: nil, wantErr: true, wantInPriv: "empty ndjson"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		rows := tc.rows
		got, err := extractNDJSON(&rows)()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.wantInPriv) {
				t.Errorf("%s: Private=%q missing %q", tc.name, priv, tc.wantInPriv)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractCSV asserts the csv extract closure returns the body
// column on a well-formed table and the typed sentinel otherwise.
func TestExtractCSV(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	innerStr := string(inner)
	type tc struct {
		name       string
		table      [][]string
		want       []byte
		wantErr    bool
		wantInPriv string
	}
	tests := []tc{
		{name: "well-formed", table: [][]string{{csvPromotionHeader}, {innerStr}}, want: inner, wantErr: false},
		{name: "missing-body", table: [][]string{{csvPromotionHeader}}, wantErr: true, wantInPriv: "malformed csv"},
		{name: "empty-body-row", table: [][]string{{csvPromotionHeader}, {}}, wantErr: true, wantInPriv: "malformed csv"},
		//: the wrap stage writes exactly two single-column rows under its own
		//: header; each of these used to decode, dropping what was extra.
		{name: "an extra row", table: [][]string{{csvPromotionHeader}, {innerStr}, {"x"}}, wantErr: true, wantInPriv: "malformed csv"},
		{name: "an extra column", table: [][]string{{csvPromotionHeader}, {innerStr, "x"}}, wantErr: true, wantInPriv: "malformed csv"},
		{name: "another header", table: [][]string{{"role"}, {innerStr}}, wantErr: true, wantInPriv: "malformed csv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		table := tc.table
		got, err := extractCSV(&table)()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.wantInPriv) {
				t.Errorf("%s: Private=%q missing %q", tc.name, priv, tc.wantInPriv)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractForm asserts the form extract closure returns the promotion
// pair's value on a well-formed body and the typed sentinel otherwise. A second
// key used to pass: _json=…&role=user decoded the JSON and dropped role without
// a word — seen failing so, with the key count check removed.
func TestExtractForm(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	innerStr := string(inner)
	type tc struct {
		name    string
		values  url.Values
		want    []byte
		wantErr bool
	}
	tests := []tc{
		{name: "well-formed", values: url.Values{formPromotionKey: {innerStr}}, want: inner},
		{name: "no promotion pair", values: url.Values{"role": {"user"}}, wantErr: true},
		{name: "a repeated promotion pair", values: url.Values{formPromotionKey: {innerStr, innerStr}}, wantErr: true},
		{name: "a second key beside the pair", values: url.Values{formPromotionKey: {innerStr}, "role": {"user"}}, wantErr: true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		values := tc.values
		got, err := extractForm(&values)()
		if tc.wantErr {
			if priv := kerrs.PrivateOf(err); err == nil || !strings.Contains(priv, "malformed form") {
				t.Fatalf("%s: err=%v, want the malformed-form refusal", tc.name, err)
			}
			return
		}
		if err != nil || string(got) != string(tc.want) {
			t.Fatalf("%s: got=%q err=%v, want %q", tc.name, got, err, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractPEM asserts the pem extract closure returns block.Bytes
// on a well-formed block and the typed sentinel on a nil block.
func TestExtractPEM(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	type tc struct {
		name       string
		block      *stdpem.Block
		want       []byte
		wantErr    bool
		wantInPriv string
	}
	tests := []tc{
		{name: "well-formed", block: &stdpem.Block{Type: pemPromotionBlockType, Bytes: inner}, want: inner, wantErr: false},
		{name: "nil-block", block: nil, wantErr: true, wantInPriv: "nil pem"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		block := tc.block
		got, err := extractPEM(&block)()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.wantInPriv) {
				t.Errorf("%s: Private=%q missing %q", tc.name, priv, tc.wantInPriv)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractFlatBuffers asserts the flatbuffers extract closure
// strips the 4-byte header on success and surfaces the typed sentinel
// when the payload is too short.
func TestExtractFlatBuffers(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	wrapped := make([]byte, flatBuffersHeaderBytes+len(inner))
	copy(wrapped[flatBuffersHeaderBytes:], inner)
	type tc struct {
		name       string
		raw        []byte
		want       []byte
		wantErr    bool
		wantInPriv string
	}
	tests := []tc{
		{name: "well-formed", raw: wrapped, want: inner, wantErr: false},
		{name: "too-short", raw: []byte{0x00, 0x01}, wantErr: true, wantInPriv: "flatbuffers promotion payload too short"},
		{name: "empty", raw: nil, wantErr: true, wantInPriv: "flatbuffers promotion payload too short"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		raw := tc.raw
		got, err := extractFlatBuffers(&raw)()
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.wantInPriv) {
				t.Errorf("%s: Private=%q missing %q", tc.name, priv, tc.wantInPriv)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestExtractTLV asserts the tlv extract closure casts the scalar
// string back to bytes.
func TestExtractTLV(t *testing.T) {
	t.Parallel()
	inner := canaryJSONBytes(t)
	type tc struct {
		name string
		seed string
		want []byte
	}
	tests := []tc{
		{name: "well-formed", seed: string(inner), want: inner},
		{name: "empty", seed: "", want: []byte{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		s := tc.seed
		got, err := extractTLV(&s)()
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteMarshal_UnknownFormat asserts the typed PromoteFailed
// sentinel surfaces when no strategy exists for the requested format.
func TestPromoteMarshal_UnknownFormat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format Format
	}
	tests := []tc{
		{name: "phantom", format: Format("phantom")},
		{name: "blank", format: Format("")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: c is nil because promoteMarshal must short-circuit before
		//: reaching the codec.
		_, err := promoteMarshal(tc.format, nil, promoteCanaryUser{Name: "Ada", Age: 36})
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Errorf("%s: reason=%q want PROMOTE_FAILED (err=%v)", tc.name, reason, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteUnmarshal_UnknownFormat is the symmetric counterpart for
// promoteUnmarshal.
func TestPromoteUnmarshal_UnknownFormat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format Format
	}
	tests := []tc{
		{name: "phantom", format: Format("phantom")},
		{name: "blank", format: Format("")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var back promoteCanaryUser
		//: c is nil because promoteUnmarshal must short-circuit
		//: before reaching the codec.
		err := promoteUnmarshal(tc.format, nil, []byte("ignored"), &back)
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Errorf("%s: reason=%q want PROMOTE_FAILED (err=%v)", tc.name, reason, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteMarshal_JSONError asserts promoteMarshal forwards the
// encoding/json failure verbatim when the value cannot be serialised to
// the JSON intermediate. A chan can never round-trip through json, so a
// CSV-routed chan reaches the codec's VALUE_INVALID shape-rejection but
// fails one step earlier at the json.Marshal bridge — exercising the
// "surface json failure verbatim" arm that no roundtrip fixture hits.
func TestPromoteMarshal_JSONError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format Format
		value  any
	}
	tests := []tc{
		//: chan is the canonical json-unsupported type.
		{name: "csv-chan", format: CSV, value: make(chan int)},
		//: func is the second json-unsupported scalar; ndjson is another
		//: constrained codec so the arm holds across promotion targets.
		{name: "ndjson-func", format: NDJSON, value: func() {}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := lookupCodec(t, tc.format)
		_, err := promoteMarshal(tc.format, c, tc.value)
		//: the json bridge fails before any wrap step.
		if err == nil {
			t.Fatalf("%s: want json marshal error, got nil", tc.name)
		}
		//: the failure is the raw encoding/json error, NOT a typed
		//: PromoteFailed sentinel — promoteMarshal forwards it as-is.
		if _, ok := kerrs.ReasonOf(err); ok {
			t.Errorf("%s: err=%v unexpectedly carries a typed reason; want raw json error", tc.name, err)
		}
		//: the json package names itself in the message.
		if !strings.Contains(err.Error(), "json") {
			t.Errorf("%s: err=%q does not look like an encoding/json failure", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteUnmarshal_ContainerUnmarshalError asserts promoteUnmarshal
// surfaces the codec's own decode failure when the wire bytes are
// malformed for the codec's native container. The CSV reader rejects a
// row whose field count disagrees with the header, so feeding ragged CSV
// drives the "codec failed to decode the wire bytes" arm — distinct from
// the extract-shape arm below.
func TestPromoteUnmarshal_ContainerUnmarshalError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		format Format
		data   []byte
		reason string
	}
	tests := []tc{
		//: ragged CSV (header has 3 cols, body has 2) fails ReadAll.
		{name: "csv-ragged", format: CSV, data: []byte("a,b,c\n1,2\n"), reason: "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := lookupCodec(t, tc.format)
		var back promoteCanaryUser
		err := promoteUnmarshal(tc.format, c, tc.data, &back)
		//: the codec rejected the wire bytes before extract could run.
		if err == nil {
			t.Fatalf("%s: want codec decode error, got nil", tc.name)
		}
		//: the surfaced reason is the codec's own UNMARSHAL_FAILED, not
		//: the promotion container sentinel.
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != tc.reason {
			t.Errorf("%s: reason=%q ok=%v want %q", tc.name, reason, ok, tc.reason)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPromoteUnmarshal_ExtractError asserts promoteUnmarshal surfaces the
// typed PromoteFailed container sentinel when the codec decodes the wire
// bytes cleanly but into a shape the extract closure rejects. A single-
// row CSV parses fine yet lacks the header+body pair the csv extract
// requires, so this drives the "container shape mismatch" arm that sits
// after a SUCCESSFUL inner Unmarshal.
func TestPromoteUnmarshal_ExtractError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  Format
		data    []byte
		private string
	}
	tests := []tc{
		//: a lone header row parses to a 1-row table; extractCSV needs 2.
		{name: "csv-single-row", format: CSV, data: []byte("onlyheader\n"), private: "malformed csv"},
		//: an empty ndjson stream parses to a 0-row slice; extractNDJSON
		//: needs at least one record.
		{name: "ndjson-empty", format: NDJSON, data: []byte(""), private: "empty ndjson"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := lookupCodec(t, tc.format)
		var back promoteCanaryUser
		err := promoteUnmarshal(tc.format, c, tc.data, &back)
		//: extract rejected the populated container shape.
		if err == nil {
			t.Fatalf("%s: want container-shape error, got nil", tc.name)
		}
		//: the typed sentinel keeps PROMOTE_FAILED routing intact.
		if reason, ok := kerrs.ReasonOf(err); !ok || reason != "PROMOTE_FAILED" {
			t.Fatalf("%s: reason=%q want PROMOTE_FAILED (err=%v)", tc.name, reason, err)
		}
		//: Private names the malformed container so triage is precise.
		if priv := kerrs.PrivateOf(err); !strings.Contains(priv, tc.private) {
			t.Errorf("%s: Private=%q missing %q", tc.name, priv, tc.private)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
