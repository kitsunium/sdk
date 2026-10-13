package wire_test

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// FuzzParseMessage: the frame decoder never panics, and what it accepts
// encodes back to the same bytes (D15 fuzz targets).
func FuzzParseMessage(f *testing.F) {
	m := &coreconsensus.Message{
		Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 3,
		Entries: []coreconsensus.Entry{{Position: coreconsensus.Position{Term: 3, Index: 4}, Kind: coreconsensus.EntryCommand, Data: []byte("x")}},
	}
	f.Add(wire.AppendMessage(nil, m))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		var out coreconsensus.Message
		if err := wire.ParseMessage(b, &out); err != nil {
			return
		}
		if again := wire.AppendMessage(nil, &out); !bytes.Equal(again, b) {
			t.Fatalf("accepted frame does not round-trip")
		}
	})
}

// FuzzParseSnapshotMeta: the snapshot metadata decoder never panics and
// round-trips what it accepts.
func FuzzParseSnapshotMeta(f *testing.F) {
	m := &coreconsensus.SnapshotMeta{ID: "1-2", Version: 1, Position: coreconsensus.Position{Term: 2, Index: 1}, Codec: "flate"}
	f.Add(wire.AppendSnapshotMeta(nil, m))
	f.Fuzz(func(t *testing.T, b []byte) {
		var out coreconsensus.SnapshotMeta
		if err := wire.ParseSnapshotMeta(b, &out); err != nil {
			return
		}
		if again := wire.AppendSnapshotMeta(nil, &out); !bytes.Equal(again, b) {
			t.Fatalf("accepted meta does not round-trip")
		}
	})
}

// FuzzParseAdmin: the admin payload decoder never panics. A refusal is the
// expected outcome for most inputs and carries nothing to assert; the
// property under test is that the call returns at all.
func FuzzParseAdmin(f *testing.F) {
	f.Add(wire.AppendAdmin(nil, &wire.Admin{Operator: "op", Change: wire.ChangeRemove, Target: 9}))
	f.Fuzz(func(_ *testing.T, b []byte) {
		if _, err := wire.ParseAdmin(b); err != nil {
			return
		}
	})
}

// FuzzParseToken: the token decoder never panics, and a token it accepts
// encodes back to the same bytes — the encoding is fixed-width with no
// optional field, so an accepted token has exactly one spelling.
func FuzzParseToken(f *testing.F) {
	f.Add(wire.AppendToken(nil, coreconsensus.Token{Epoch: 1, Term: 2, Index: 3}))
	f.Fuzz(func(t *testing.T, b []byte) {
		tok, err := wire.ParseToken(b)
		if err != nil {
			return
		}
		if again := wire.AppendToken(nil, tok); !bytes.Equal(again, b) {
			t.Fatalf("accepted token does not round-trip")
		}
	})
}
