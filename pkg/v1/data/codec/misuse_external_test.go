package codec_test

import (
	"bytes"
	"testing"
	"unsafe"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// misuseSample is a value every Format carries, natively or promoted.
type misuseSample struct {
	A int    `json:"a" csv:"a"`
	B string `json:"b" csv:"b"`
}

// TestEveryTargetMisuseIsTargetInvalid is #1101's acceptance: for every
// registered Format, Unmarshal of valid bytes into nil, into a typed nil
// pointer and into a value that is not a pointer is refused with
// TARGET_INVALID — a programmer's error, never corrupt input — and so is a
// streaming Decode into nil.
func TestEveryTargetMisuseIsTargetInvalid(t *testing.T) {
	t.Parallel()
	for _, f := range codec.Available() {
		valid, err := codec.Marshal(f, misuseSample{A: 1, B: "x"})
		if err != nil {
			t.Errorf("%s: Marshal: %v", f, err)
			continue
		}
		for name, target := range map[string]any{
			"nil":         nil,
			"typed nil":   (*misuseSample)(nil),
			"non-pointer": misuseSample{},
		} {
			err := codec.Unmarshal(f, valid, target)
			if !errs.HasCode(err, corecodec.CodeTargetInvalid) || !codec.IsMisuse(err) {
				t.Errorf("%s: Unmarshal(valid, %s) = %v, want TARGET_INVALID", f, name, err)
			}
		}
		dec, err := codec.NewDecoder(f, bytes.NewReader(valid))
		if err != nil {
			continue
		}
		if err := dec.Decode(nil); !errs.HasCode(err, corecodec.CodeTargetInvalid) {
			t.Errorf("%s: Decode(nil) = %v, want TARGET_INVALID", f, err)
		}
	}
}

// TestEmptyInputIsEmptyInput: the formats with no empty document refuse zero
// bytes with EMPTY_INPUT before parsing.
func TestEmptyInputIsEmptyInput(t *testing.T) {
	t.Parallel()
	for _, f := range []codec.Format{codec.JSON, codec.XML, codec.BSON, codec.CBOR, codec.MsgPack, codec.TLV} {
		for _, in := range [][]byte{nil, {}} {
			err := codec.Unmarshal(f, in, new(any))
			if !errs.HasCode(err, corecodec.CodeEmptyInput) || !codec.IsMisuse(err) {
				t.Errorf("%s: Unmarshal(%#v) = %v, want EMPTY_INPUT", f, in, err)
			}
		}
	}
}

// TestUnencodableValueIsValueInvalid: the codecs that encode any Go value
// refuse a channel, a function and an unsafe pointer with VALUE_INVALID.
func TestUnencodableValueIsValueInvalid(t *testing.T) {
	t.Parallel()
	var x int
	values := map[string]any{"chan": make(chan int), "func": func() {}, "unsafe pointer": unsafe.Pointer(&x)}
	for _, f := range []codec.Format{codec.JSON, codec.XML, codec.YAML, codec.TOML, codec.CBOR, codec.MsgPack, codec.BSON} {
		for name, v := range values {
			_, err := codec.Marshal(f, v)
			if !errs.HasCode(err, corecodec.CodeValueInvalid) || !codec.IsMisuse(err) {
				t.Errorf("%s: Marshal(%s) = %v, want VALUE_INVALID", f, name, err)
			}
		}
	}
}

// TestIsMisuseIsNotADecodeFailure: a malformed input is not misuse.
func TestIsMisuseIsNotADecodeFailure(t *testing.T) {
	t.Parallel()
	if err := codec.Unmarshal(codec.JSON, []byte("{"), new(any)); err == nil || codec.IsMisuse(err) {
		t.Errorf("Unmarshal(JSON, \"{\") = %v; want a decode failure, not misuse", err)
	}
}
