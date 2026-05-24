package codec

import (
	"encoding/binary"
	stdjson "encoding/json"
	stdpem "encoding/pem"
	"errors"
	"strings"
	"testing"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

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
				//: 4-byte header is little-endian zero offset.
				if binary.LittleEndian.Uint32(raw[:flatBuffersHeaderBytes]) != 0 {
					t.Errorf("flatbuffers wrap header non-zero: % x", raw[:flatBuffersHeaderBytes])
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
	type tc struct {
		name       string
		table      [][]string
		want       []byte
		wantErr    bool
		wantInPriv string
	}
	tests := []tc{
		{name: "well-formed", table: [][]string{{csvPromotionHeader}, {string(inner)}}, want: inner, wantErr: false},
		{name: "missing-body", table: [][]string{{csvPromotionHeader}}, wantErr: true, wantInPriv: "malformed csv"},
		{name: "empty-body-row", table: [][]string{{csvPromotionHeader}, {}}, wantErr: true, wantInPriv: "malformed csv"},
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
