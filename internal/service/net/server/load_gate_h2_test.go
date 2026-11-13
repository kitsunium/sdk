//go:build linux || darwin

// L1h2, the HTTP/2 lane of the merge gate (#963): L1's request over h2c by
// prior knowledge, loadConns kept-alive connections, h2LoadStreams streams in
// flight on each. The client writes pre-encoded frames and counts the
// END_STREAM DATA frames it reads, so its own cost stays flat, as L1's raw
// bytes do. BenchmarkLoadL1h2 pairs net/http's h2c with the loop's HTTP/2
// adapter (P9), the loop's adapter with itself (cal-L1h2), and the loop's
// HTTP/1.1 with its HTTP/2 (h1-h2, reported).

package server_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	stdnet "net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// h2LoadStreams is how many streams one connection keeps in flight.
const h2LoadStreams = 8

// h2Preface opens an h2c connection: the client preface and an empty SETTINGS.
var h2Preface = append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), 0, 0, 0, 0x4, 0, 0, 0, 0, 0)

// h2LoadBlock is L1's request as one field block: :method GET and :scheme
// http indexed from the static table, :path and :authority literals on
// indexed names, nothing that touches a dynamic table.
var h2LoadBlock = []byte{0x82, 0x86, 0x04, 6, '/', 'h', 'e', 'l', 'l', 'o', 0x01, 7, 'n', 'e', 't', 'g', 'a', 't', 'e'}

// errH2Load is a frame the load does not expect.
var errH2Load = errors.New("h2 load: unexpected frame")

// isH2Engine reports whether an engine of the gate is served over h2c.
func isH2Engine(engine string) bool {
	return strings.HasSuffix(engine, "-h2")
}

// h2LoadConn is one kept-alive h2c connection of the load.
type h2LoadConn struct {
	c        stdnet.Conn
	br       *bufio.Reader
	stream   uint32
	consumed int
	out      []byte
	hdr      [9]byte
	scratch  []byte
}

// dialH2Load opens an h2c connection to addr and serves one batch on it.
func dialH2Load(addr string) (*h2LoadConn, error) {
	c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	h := &h2LoadConn{c: c, br: bufio.NewReaderSize(c, 16<<10), stream: 1, out: make([]byte, 0, h2LoadStreams*(9+len(h2LoadBlock))), scratch: make([]byte, 64)}
	if _, err := c.Write(h2Preface); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := h.batch(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return h, nil
}

// frame appends one frame header to h.out.
func (h *h2LoadConn) frame(typ, flags byte, stream uint32, n int) {
	h.out = append(h.out, byte(n>>16), byte(n>>8), byte(n), typ, flags)
	h.out = binary.BigEndian.AppendUint32(h.out, stream)
}

// batch sends h2LoadStreams GETs in one write and reads until each stream
// ended with its 1 KiB answer, crediting the connection window as it reads.
func (h *h2LoadConn) batch() error {
	_ = h.c.SetDeadline(time.Now().Add(10 * time.Second))
	h.out = h.out[:0]
	for range h2LoadStreams {
		h.frame(0x1, 0x5, h.stream, len(h2LoadBlock))
		h.out = append(h.out, h2LoadBlock...)
		h.stream += 2
	}
	if _, err := h.c.Write(h.out); err != nil {
		return err
	}
	ended, body := 0, 0
	for ended < h2LoadStreams {
		n, typ, flags, err := h.readHeader()
		if err != nil {
			return err
		}
		if err := h.handle(n, typ, flags, &ended, &body); err != nil {
			return err
		}
	}
	if body != h2LoadStreams*loadBodySize {
		return errH2Load
	}
	return nil
}

// readHeader reads one frame header.
func (h *h2LoadConn) readHeader() (n int, typ, flags byte, err error) {
	if _, err = io.ReadFull(h.br, h.hdr[:]); err != nil {
		return 0, 0, 0, err
	}
	return int(h.hdr[0])<<16 | int(h.hdr[1])<<8 | int(h.hdr[2]), h.hdr[3], h.hdr[4], nil
}

// handle consumes one frame's payload.
func (h *h2LoadConn) handle(n int, typ, flags byte, ended, body *int) error {
	switch typ {
	case 0x0:
		*body += n
		h.consumed += n
		if flags&0x1 != 0 {
			*ended++
		}
		if _, err := h.br.Discard(n); err != nil {
			return err
		}
		return h.credit()
	case 0x1:
		if flags&0x1 != 0 || n < 1 {
			return errH2Load
		}
		head, err := h.br.Peek(min(n, 5))
		if err != nil {
			return err
		}
		if head[0] != 0x88 && !bytes.HasPrefix(head, []byte{0x18, 0x03, '2', '0', '0'}) {
			return errH2Load
		}
		_, err = h.br.Discard(n)
		return err
	case 0x4, 0x6:
		return h.control(n, typ, flags)
	case 0x3, 0x7:
		return errH2Load
	default:
		_, err := h.br.Discard(n)
		return err
	}
}

// control answers a SETTINGS with its ACK and a PING with its own, and reads
// past their ACKs.
func (h *h2LoadConn) control(n int, typ, flags byte) error {
	if n > len(h.scratch) {
		h.scratch = make([]byte, n)
	}
	if _, err := io.ReadFull(h.br, h.scratch[:n]); err != nil {
		return err
	}
	if flags&0x1 != 0 {
		return nil
	}
	h.out = h.out[:0]
	if typ == 0x4 {
		h.frame(0x4, 0x1, 0, 0)
	} else {
		h.frame(0x6, 0x1, 0, n)
		h.out = append(h.out, h.scratch[:n]...)
	}
	_, err := h.c.Write(h.out)
	return err
}

// credit returns half a default window to the connection once it was read.
func (h *h2LoadConn) credit() error {
	if h.consumed < 32<<10 {
		return nil
	}
	h.out = h.out[:0]
	h.frame(0x8, 0, 0, 4)
	h.out = binary.BigEndian.AppendUint32(h.out, uint32(h.consumed))
	h.consumed = 0
	_, err := h.c.Write(h.out)
	return err
}

// h2Side is one engine of a pair over h2c.
type h2Side struct {
	conns []*h2LoadConn
}

// dialH2Side opens loadConns h2c connections to addr, each served once.
func dialH2Side(b testing.TB, addr string, conns int) *h2Side {
	s := &h2Side{}
	for i := range conns {
		c, err := dialH2Load(addr)
		if err != nil {
			b.Fatalf("h2 dial %d: %v", i, err)
		}
		s.conns = append(s.conns, c)
	}
	b.Cleanup(func() {
		for _, c := range s.conns {
			_ = c.c.Close()
		}
	})
	return s
}

// window sends n requests in batches of h2LoadStreams over the side's
// connections and returns the child's CPU it took.
func (s *h2Side) window(b testing.TB, child *loadChild, n int) uint64 {
	before := child.sample(b)
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, len(s.conns))
	for _, c := range s.conns {
		wg.Go(func() {
			for next.Add(h2LoadStreams) <= int64(n)+h2LoadStreams-1 {
				if err := c.batch(); err != nil {
					errc <- err
					return
				}
			}
		})
	}
	wg.Wait()
	after := child.sample(b)
	close(errc)
	if err := <-errc; err != nil {
		b.Fatal(err)
	}
	return after[0] - before[0]
}

// BenchmarkLoadL1h2 is L1h2 paired: net/http's h2c against the loop's HTTP/2
// adapter (P9), the adapter against itself (cal-L1h2), and the loop's
// HTTP/1.1 against its HTTP/2 on one child (h1-h2, reported).
func BenchmarkLoadL1h2(b *testing.B) {
	for _, p := range [][2]string{{"goroutine-h2", "loop-h2"}, {"loop-h2", "loop-h2"}, {"loop", "loop-h2"}} {
		b.Run("pair="+p[0]+":"+p[1], func(b *testing.B) { benchPairedLoad(b, "L1h2", p[0], p[1], loadL1Request) })
	}
}

// TestLoadL1h2_wired serves each L1h2 engine in its child for one batch of
// streams and reads its counter, so a harness that no longer serves HTTP/2
// fails the suite, not only the bench the suite never runs.
func TestLoadL1h2_wired(t *testing.T) {
	for _, engine := range []string{"goroutine-h2", "loop-h2"} {
		t.Run("L1h2/"+engine, func(t *testing.T) {
			child := startLoadChild(t, "L1h2", engine)
			side := dialH2Side(t, child.addrs[0], 2)
			if cpu := side.window(t, child, 4*h2LoadStreams); cpu == 0 {
				t.Fatal("the child's CPU did not move over four batches")
			}
		})
	}
}
