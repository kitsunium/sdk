package wire_test

import (
	"errors"
	"reflect"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A forward carries its proposer's trace context, 25 bytes after its entry,
// exactly when FlagTrace is set, and is read back as written; without the
// flag the frame is the layout a node of protocol 3 parses, byte for byte.
// The flag on another kind, a short context or a non-canonical sampled byte
// is FRAME_INVALID (#1115, ADR 0152 D13).
func TestAForwardCarriesItsTraceContextOnlyBesideFlagTrace(t *testing.T) {
	fwd := func(flags coreconsensus.MessageFlags) *coreconsensus.MessageValue {
		return &coreconsensus.MessageValue{
			Version: 1, Group: 1, Kind: coreconsensus.MsgForward, Flags: flags, From: 2, To: 1, Term: 3, Context: 7,
			Entries: []coreconsensus.EntryValue{{Kind: coreconsensus.EntryCommand, Data: []byte("x")}},
		}
	}
	plain := wire.AppendMessage(nil, fwd(0))
	traced := fwd(coreconsensus.FlagTrace)
	traced.Trace = coreconsensus.TraceContextValue{TraceID: [16]byte{1, 2, 3}, SpanID: [8]byte{4, 5}, Sampled: true}
	b := wire.AppendMessage(nil, traced)
	if len(b) != len(plain)+25 {
		t.Fatalf("traced forward is %d bytes, want %d", len(b), len(plain)+25)
	}
	var out coreconsensus.MessageValue
	if err := wire.ParseMessage(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, *traced) {
		t.Fatalf("read back %+v, want %+v", out, *traced)
	}
	// Without the flag the context is not written, whatever the value holds.
	untraced := fwd(0)
	untraced.Trace = traced.Trace
	if got := wire.AppendMessage(nil, untraced); !reflect.DeepEqual(got, plain) {
		t.Fatal("a forward without FlagTrace wrote trace bytes")
	}
	for name, frame := range map[string][]byte{
		"trace flag on an append": func() []byte {
			m := fwd(coreconsensus.FlagTrace)
			m.Kind = coreconsensus.MsgApp
			return wire.AppendMessage(nil, m)
		}(),
		"short context":        withLength(b[:len(b)-1]),
		"non-canonical bool":   func() []byte { c := append([]byte(nil), b...); c[len(c)-1] = 2; return c }(),
		"flag with no bytes":   func() []byte { c := append([]byte(nil), plain...); c[14] |= byte(coreconsensus.FlagTrace); return c }(),
		"bytes with no flag":   withLength(append(append([]byte(nil), plain...), b[len(plain):]...)),
		"trailing after trace": withLength(append(append([]byte(nil), b...), 0)),
	} {
		if err := wire.ParseMessage(frame, &out); !errors.Is(err, coreconsensus.FrameInvalid) {
			t.Errorf("%s: %v, want FRAME_INVALID", name, err)
		}
	}
}

// withLength rewrites a frame's length field to its own length.
func withLength(b []byte) []byte {
	c := append([]byte(nil), b...)
	n := uint32(len(c) - 4)
	c[0], c[1], c[2], c[3] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	return c
}
