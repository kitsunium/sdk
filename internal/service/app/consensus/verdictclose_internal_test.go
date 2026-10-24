package consensus

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

// #654: the sender half-closes a snapshot stream after the body, and its
// TLS close_notify was still unread in the receiver's buffer when the
// receiver closed after its verdict: the close went out as a RST, and
// Windows drops the data a RST overtakes, the verdict among it. Every TCP
// Stream on the windows runners failed FRAME_INVALID "install result" on
// an install that had succeeded. The receiver now reads the sender's end
// before it closes, and ends the connection with a FIN, the verdict ahead
// of it on every platform.
func TestASnapshotReceiverEndsTheStreamWithoutAReset(t *testing.T) {
	p := newOfferPair(t, 0, 0)
	addr, pin, _ := p.trL.address(2)
	for i := range 20 {
		c, err := p.trL.dial(context.Background(), addr, laneSnapshot, &pin)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeChunk(c, encodeOffer(retiredOffer(p.members, 0))); err != nil {
			t.Fatal(err)
		}
		acc, err := readAccept(c, time.Second)
		if err != nil || binary.LittleEndian.Uint64(acc) != 0 {
			t.Fatalf("accept: %v %v", acc, err)
		}
		if _, err := c.Write(retiredSnapshot); err != nil {
			t.Fatal(err)
		}
		if err := c.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		// The receiver answers and closes meanwhile: its FIN, or its RST,
		// is in before the sender reads.
		time.Sleep(20 * time.Millisecond)
		if err := installResult(c); err != nil {
			t.Fatalf("stream %d: the verdict of an install that succeeded: %v", i, err)
		}
		var b [1]byte
		if _, err := c.Read(b[:]); !errors.Is(err, io.EOF) {
			t.Fatalf("stream %d: the receiver ended the connection with %v, want a clean end (io.EOF)", i, err)
		}
		abandon(c.Close())
	}
}
