package rpc_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc/rpc"
)

// A 4 GiB length: what a hostile peer writes to make its reader allocate.
var fourGiB = []byte{0xFF, 0xFF, 0xFF, 0xFF}

// rawHello is a hello written by hand, as ADR 0218 lays it out: len | kind 1 |
// id 0 | mlen | format | version 1 | maxFrame.
func rawHello(format string, maxFrame uint32) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(10+len(format)+5))
	b = append(b, 1)
	b = binary.BigEndian.AppendUint64(b, 0)
	b = append(b, byte(len(format)))
	b = append(b, format...)
	b = append(b, 1)
	return binary.BigEndian.AppendUint32(b, maxFrame)
}

// readRawFrame reads one whole frame off r, the test's side of a pipe.
func readRawFrame(t *testing.T, r io.Reader) (kind byte, body []byte) {
	t.Helper()
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		t.Fatalf("reading a frame's length: %v", err)
	}
	buf := make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading a frame: %v", err)
	}
	return buf[0], buf[10+int(buf[9]):]
}

// writeRaw writes b to the test's side of a pipe.
func writeRaw(t *testing.T, w io.Writer, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// helloServer serves one pipe connection and says hello on the test's side.
func helloServer(t *testing.T, cfg rpc.ServerConfig) (net.Conn, chan error) {
	t.Helper()
	srv, err := rpc.NewServer(cfg, rpc.Methods{"echo": rpc.HandlerFunc(echo)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeOrLog(t, srv) })
	client, server := net.Pipe()
	t.Cleanup(func() { closeOrLog(t, client) })
	served := make(chan error, 1)
	go func() { served <- srv.ServeConn(context.Background(), &coreipc.Conn{Conn: server, Peer: testPeer}) }()
	writeRaw(t, client, rawHello("json", 64<<10))
	if kind, _ := readRawFrame(t, client); kind != 1 {
		t.Fatalf("the server answered the hello with kind %d", kind)
	}
	return client, served
}

// TestA4GiBLengthIsRefusedBeforeAnyBodyIsAllocated is #930's acceptance: a
// frame whose length says 4 GiB closes the connection with FRAME_TOO_LARGE
// on the length alone — the server reads nothing past it, and the process
// allocates nowhere near the length while it refuses. No t.Parallel: it reads
// the process-wide allocation counter.
func TestA4GiBLengthIsRefusedBeforeAnyBodyIsAllocated(t *testing.T) {
	client, served := helloServer(t, serverConfig(4))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	writeRaw(t, client, fourGiB)
	kind, body := readRawFrame(t, client)
	err := <-served
	runtime.ReadMemStats(&after)
	if !errs.HasCode(err, corerpc.CodeFrameTooLarge) {
		t.Fatalf("ServeConn: %v, want FRAME_TOO_LARGE", err)
	}
	if kind != 7 || errs.Code(binary.BigEndian.Uint32(body)) != corerpc.CodeFrameTooLarge {
		t.Fatalf("the peer was told kind %d, code %x; want an error frame with FRAME_TOO_LARGE", kind, body[:4])
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 256<<10 {
		t.Fatalf("refusing a 4 GiB length allocated %d bytes, want under 256 KiB", grew)
	}
}

// TestAClientRefusesA4GiBAnswer: the same bound holds on the client's side;
// the call in flight ends with FRAME_TOO_LARGE.
func TestAClientRefusesA4GiBAnswer(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() { closeOrLog(t, server) })
	hostile := make(chan error, 1)
	go func() {
		hostile <- errors.Join(skipFrame(server), writeAll(server, rawHello("json", 64<<10)),
			skipFrame(server), writeAll(server, fourGiB))
	}()
	c, err := rpc.NewClient(context.Background(), constDialer{&coreipc.Conn{Conn: client}}, clientConfig(4))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer closeOrLog(t, c)
	if err := c.Call(context.Background(), "echo", nil, nil); !errs.HasCode(err, corerpc.CodeFrameTooLarge) {
		t.Fatalf("Call: %v, want FRAME_TOO_LARGE", err)
	}
	if err := <-hostile; err != nil {
		t.Fatalf("the hostile server: %v", err)
	}
}

// skipFrame reads one whole frame off r and drops it; a goroutine's helper,
// which may not call t.Fatal.
func skipFrame(r io.Reader) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, r, int64(binary.BigEndian.Uint32(prefix[:])))
	return err
}

// writeAll writes b to w; a goroutine's helper.
func writeAll(w io.Writer, b []byte) error {
	_, err := w.Write(b)
	return err
}

// constDialer hands out one connection.
type constDialer struct{ c *coreipc.Conn }

func (d constDialer) Dial(context.Context) (*coreipc.Conn, error) { return d.c, nil }

// TestHalfAFrameIsClosedAfterFrameTimeout: an idle connection stays open for
// as long as it likes, but a frame begun must be whole within FrameTimeout on
// the server's clock, or the connection closes with PROTOCOL_VIOLATION.
func TestHalfAFrameIsClosedAfterFrameTimeout(t *testing.T) {
	t.Parallel()
	sclk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	cfg := serverConfig(4)
	cfg.Clock, cfg.FrameTimeout = sclk, time.Second
	client, served := helloServer(t, cfg)
	//: idle for ten times FrameTimeout, in steps shorter than it: the hello
	//: answer's write, which may still be finishing, never sees its deadline.
	for range 10 {
		sclk.Advance(900 * time.Millisecond)
		select {
		case err := <-served:
			t.Fatalf("an idle connection was closed: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	writeRaw(t, client, []byte{0, 0})
	if err := advanceUntil(t, sclk, time.Second, served); !errs.HasCode(err, corerpc.CodeProtocolViolation) {
		t.Fatalf("ServeConn: %v, want PROTOCOL_VIOLATION", err)
	}
}

// TestAFrameOutOfPlaceEndsTheConnection: a reply sent to a server, and an id
// reused while in flight, are violations.
func TestAFrameOutOfPlaceEndsTheConnection(t *testing.T) {
	t.Parallel()
	client, served := helloServer(t, serverConfig(4))
	reply := binary.BigEndian.AppendUint32(nil, 10)
	reply = append(reply, 4, 0, 0, 0, 0, 0, 0, 0, 1, 0)
	writeRaw(t, client, reply)
	if kind, _ := readRawFrame(t, client); kind != 7 {
		t.Fatalf("the server answered kind %d, want an error frame", kind)
	}
	if err := <-served; !errs.HasCode(err, corerpc.CodeProtocolViolation) {
		t.Fatalf("ServeConn: %v, want PROTOCOL_VIOLATION", err)
	}
}
