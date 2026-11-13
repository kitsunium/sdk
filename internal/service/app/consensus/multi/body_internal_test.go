package multi

import (
	"bytes"
	"encoding/binary"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

func TestABodySplitsIntoItsCommandsInOrder(t *testing.T) {
	cmds := [][]byte{[]byte("a=1"), {}, bytes.Repeat([]byte{7}, 300), []byte("b=2")}
	e := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: 3, Index: 9}, Kind: coreconsensus.EntryCommand,
		ChainSum: 42, Token: coreconsensus.TokenValue{Term: 3, Index: 9},
	}
	e.Data = appendBody(nil, cmds)
	e.BodySum = wire.BodySum(e.Data)
	size := bodyHeaderMax
	for _, c := range cmds {
		size += cmdCost(c)
	}
	if len(e.Data) > size {
		t.Fatalf("body of %d bytes past its bound %d", len(e.Data), size)
	}
	subs, err := splitBody(nil, &e, wire.BodySum)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != len(cmds) {
		t.Fatalf("%d commands, want %d", len(subs), len(cmds))
	}
	for i, s := range subs {
		if !bytes.Equal(s.Data, cmds[i]) || cap(s.Data) != len(s.Data) {
			t.Fatalf("command %d: %q cap %d", i, s.Data, cap(s.Data))
		}
		if s.BodySum != wire.BodySum(cmds[i]) || s.ChainSum != 0 || s.Position != e.Position || s.Token != e.Token {
			t.Fatalf("command %d: %+v", i, s)
		}
	}
}

func TestABodyThatDoesNotParseIsFrameInvalid(t *testing.T) {
	good := appendBody(nil, [][]byte{[]byte("x")})
	type tc struct {
		name string
		body []byte
	}
	cases := []tc{
		{"empty", nil},
		{"version", append([]byte{2}, good[1:]...)},
		{"no count", []byte{bodyVersion}},
		{"count past the bytes", binary.AppendUvarint([]byte{bodyVersion}, 1<<40)},
		{"length past the bytes", []byte{bodyVersion, 1, 5, 'a'}},
		{"trailing bytes", append(append([]byte{}, good...), 0)},
		{"missing command", []byte{bodyVersion, 2, 1, 'a'}},
	}
	runCase := func(t *testing.T, c tc) {
		e := coreconsensus.EntryValue{Data: c.body}
		dst := []coreconsensus.EntryValue{{}}
		got, err := splitBody(dst, &e, wire.BodySum)
		if !errs.HasCode(err, coreconsensus.CodeFrameInvalid) {
			t.Fatalf("err %v, want FRAME_INVALID", err)
		}
		if len(got) != 1 {
			t.Fatalf("dst grew to %d", len(got))
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
