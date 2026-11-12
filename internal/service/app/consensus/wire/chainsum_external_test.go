package wire_test

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// ChainSum is the CRC-32C of the previous chain value and the entry's
// header fields, whatever loop computes it: the sum written on disk and on
// the wire before #921 made it allocation-free must stay the sum written
// after it.
func TestChainSumIsTheCRC32COfItsHeader(t *testing.T) {
	castagnoli := crc32.MakeTable(crc32.Castagnoli)
	prev := uint32(0)
	for i := range 1000 {
		e := coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: uint64(i) * 7919, Index: uint64(i)<<33 | uint64(i)},
			Kind:     coreconsensus.EntryKind(i % 5), Version: uint32(i * 31), BodySum: uint32(i) * 2654435761,
		}
		var b [29]byte
		binary.LittleEndian.PutUint32(b[0:], prev)
		binary.LittleEndian.PutUint64(b[4:], e.Position.Term)
		binary.LittleEndian.PutUint64(b[12:], e.Position.Index)
		b[20] = byte(e.Kind)
		binary.LittleEndian.PutUint32(b[21:], e.Version)
		binary.LittleEndian.PutUint32(b[25:], e.BodySum)
		want := crc32.Checksum(b[:], castagnoli)
		if got := wire.ChainSum(prev, &e); got != want {
			t.Fatalf("entry %d: ChainSum = %#x, want the CRC-32C of its header %#x", i, got, want)
		}
		prev = want
	}
}
