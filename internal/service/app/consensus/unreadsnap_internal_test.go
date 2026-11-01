package consensus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"testing/iotest"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #789: a chunk stream the reader cannot read is the disk's failure, not a
// corrupt snapshot. Read as SNAPSHOT_CORRUPT, an EIO inside the chunks let
// Recover set aside an interrupted install as one that fails its checksums
// and plan from the short log below it, while a start, the disk reading
// again, restored it and dropped the new history. A stream that ends short
// is still SNAPSHOT_CORRUPT, and one that ends on a chunk boundary io.EOF.
func TestAChunkStreamThatCannotBeReadIsAStorageFailure(t *testing.T) {
	codec, _ := defaultCodec()
	var header [chunkHeaderLen]byte
	binary.LittleEndian.PutUint32(header[0:], 10)
	binary.LittleEndian.PutUint32(header[4:], 10)
	eio := errors.New("read snap: input/output error")
	cases := []struct {
		name string
		r    io.Reader
		code errs.Code
	}{
		{name: "the header unreadable", r: iotest.ErrReader(eio), code: coreconsensus.CodeStorageFailed},
		{name: "the body unreadable", r: io.MultiReader(bytes.NewReader(header[:]), iotest.ErrReader(eio)), code: coreconsensus.CodeStorageFailed},
		{name: "the header cut short", r: bytes.NewReader(header[:5]), code: coreconsensus.CodeSnapshotCorrupt},
		{name: "the body cut short", r: io.MultiReader(bytes.NewReader(header[:]), bytes.NewReader([]byte{1, 2})), code: coreconsensus.CodeSnapshotCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := io.Copy(io.Discard, newChunkReader(tc.r, codec, 1<<10, 1<<20))
			if !errs.HasCode(err, tc.code) {
				t.Fatalf("the read failed with %v, want code %v", err, tc.code)
			}
		})
	}
	if n, err := newChunkReader(bytes.NewReader(nil), codec, 1<<10, 1<<20).Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Fatalf("an empty stream read %d bytes, %v; want io.EOF", n, err)
	}
}
