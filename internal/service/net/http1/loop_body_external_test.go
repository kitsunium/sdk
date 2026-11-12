package http1_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// bodySink is an HTTP1BodyReader that records what it is given.
type bodySink struct {
	// chunks are the chunks' lengths and bytes received, per request.
	chunks int
	body   []byte
	lasts  int
	// targets are the head's target as OnBody saw it, each call.
	targets []string
	aborted int
	served  int
	// bodyAtServe is req.Body when ServeHTTP1 ran for the upload.
	bodyAtServe  []byte
	uploadServed int
	closeResp    bool
}

func (b *bodySink) OnBody(req *corenet.HTTP1RequestValue, chunk []byte, last bool) {
	b.chunks++
	b.body = append(b.body, chunk...)
	b.targets = append(b.targets, string(req.Target))
	if last {
		b.lasts++
	}
}

func (b *bodySink) AbortBody(*corenet.HTTP1RequestValue) { b.aborted++ }

func (b *bodySink) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	b.served++
	if string(req.Target) == "/upload" {
		b.bodyAtServe, b.uploadServed = req.Body, b.uploadServed+1
	}
	res.Status = 200
	res.Close = b.closeResp
}

// plainH takes its bodies whole.
type plainH struct{}

func (plainH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Status = 200
}

// post returns a head declaring n bytes of body.
func post(target string, n int) string {
	return "POST " + target + " HTTP/1.1\r\nHost: h\r\nContent-Length: " + strconv.Itoa(n) + "\r\n\r\n"
}

// TestCheckSpill_defaultBoundCappedAtTheSpill pins a default bound and an
// explicit one against a spill: the default capped at the room beside the head,
// so a body past it is refused 413 on the head and a body within it is served;
// an explicit bound past the spill's room refused, naming body.
func TestCheckSpill_defaultBoundCappedAtTheSpill(t *testing.T) {
	t.Parallel()
	const spill = 64 << 10
	a := http1.HandleLoop(plainH{}, http1.Parser{})
	if err := a.CheckSpill(spill); err != nil {
		t.Fatalf("CheckSpill on the default bound: %v", err)
	}
	io := newFakeIO(0)
	refused := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, refused)
	a.OnData(io, refused, []byte(post("/big", spill-1)))
	a.OnBatchEnd(io)
	if got := string(io.out[refused]); !strings.HasPrefix(got, "HTTP/1.1 413") {
		t.Fatalf("a body past the spill = %q, want 413", got)
	}
	if !io.closed[refused] {
		t.Error("the refused connection stayed open")
	}
	within := corenet.LoopConnRef{Slot: 2, Gen: 1}
	a.OnOpen(io, within)
	body := bytes.Repeat([]byte{'y'}, spill-corenet.HTTP1DefaultHead)
	a.OnData(io, within, append([]byte(post("/fits", len(body))), body...))
	a.OnBatchEnd(io)
	if got := string(io.out[within]); !strings.HasPrefix(got, "HTTP/1.1 200") {
		t.Fatalf("a body within the spill = %q, want 200", got)
	}
	// The default stays the default where the spill holds more than it.
	big := http1.HandleLoop(plainH{}, http1.Parser{})
	if err := big.CheckSpill(8 << 20); err != nil {
		t.Fatal(err)
	}
	p, err := http1.NewParser(corenet.HTTP1LimitsValue{Body: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	explicit := http1.HandleLoop(plainH{}, p)
	err = explicit.CheckSpill(spill)
	if !misconfiguredAs(err, "body") {
		t.Fatalf("explicit body past the spill = %v, want HTTP1Misconfigured naming body", err)
	}
	// Twice is harmless: the group's start may run again.
	if err := http1.HandleLoop(plainH{}, http1.Parser{}).CheckSpill(spill); err != nil {
		t.Fatal(err)
	}
}

// TestBodyReader_chunksAcrossOnData pins the sibling port: a 1 MiB body arriving
// in pieces on a connection reaches OnBody in as many chunks, the head valid on
// every call, the last flagged, ServeHTTP1 after it with Body nil and the answer
// written; the next pipelined request is framed from what follows the body.
func TestBodyReader_chunksAcrossOnData(t *testing.T) {
	t.Parallel()
	sink := &bodySink{}
	a := http1.HandleLoop(sink, http1.Parser{})
	if err := a.CheckSpill(64 << 10); err != nil {
		t.Fatalf("a body reader against the spill: %v", err)
	}
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	const size = 1 << 20
	head := []byte(post("/upload", size))
	if n := a.OnData(io, ref, head); n != len(head) {
		t.Fatalf("the head took %d, want %d", n, len(head))
	}
	body := bytes.Repeat([]byte{'z'}, size)
	const piece = 32 << 10
	off := 0
	for ; off+piece < size; off += piece {
		if n := a.OnData(io, ref, body[off:off+piece]); n != piece {
			t.Fatalf("a chunk took %d of %d", n, piece)
		}
		a.OnBatchEnd(io)
	}
	// The last chunk arrives with the next request behind it.
	tail := append(append([]byte(nil), body[off:]...), []byte(get("/next"))...)
	if n := a.OnData(io, ref, tail); n != len(tail) {
		t.Fatalf("the tail took %d of %d", n, len(tail))
	}
	a.OnBatchEnd(io)
	if !bytes.Equal(sink.body, body) {
		t.Fatalf("got %d bytes, want the %d sent, in order", len(sink.body), size)
	}
	if sink.chunks < 2 || sink.lasts != 1 {
		t.Fatalf("chunks %d, lasts %d", sink.chunks, sink.lasts)
	}
	for _, target := range sink.targets {
		if target != "/upload" {
			t.Fatalf("the head was not valid on every call: target %q", target)
		}
	}
	if sink.served != 2 || sink.uploadServed != 1 || sink.bodyAtServe != nil {
		t.Fatalf("served %d (upload %d), its body at ServeHTTP1 %q: want 2, 1 and nil", sink.served, sink.uploadServed, sink.bodyAtServe)
	}
	got := string(io.out[ref])
	if strings.Count(got, "HTTP/1.1 200") != 2 {
		t.Fatalf("answers = %q, want two", got)
	}
}

// TestBodyReader_refusalsStayTheParsers pins what the port does not change: a
// Content-Length past Limits.Body is 413 on the head, the body bound not the
// spill's, and a request with no body makes no OnBody call.
func TestBodyReader_refusalsStayTheParsers(t *testing.T) {
	t.Parallel()
	sink := &bodySink{}
	a := http1.HandleLoop(sink, http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte(get("/empty")))
	a.OnBatchEnd(io)
	if sink.chunks != 0 || sink.served != 1 {
		t.Fatalf("no body: chunks %d, served %d", sink.chunks, sink.served)
	}
	over := corenet.LoopConnRef{Slot: 2, Gen: 1}
	a.OnOpen(io, over)
	a.OnData(io, over, []byte(post("/big", (1<<20)+1)))
	a.OnBatchEnd(io)
	if !strings.HasPrefix(string(io.out[over]), "HTTP/1.1 413") {
		t.Fatalf("past Limits.Body: %q", io.out[over])
	}
}

// TestBodyReader_closeMidBodyAborts pins the abort: a connection that closes
// before the last chunk ends the stream, and the next connection of the slot
// starts clean.
func TestBodyReader_closeMidBodyAborts(t *testing.T) {
	t.Parallel()
	sink := &bodySink{}
	a := http1.HandleLoop(sink, http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte(post("/up", 10)+"abc"))
	a.OnClose(io, ref, nil)
	if sink.aborted != 1 || sink.lasts != 0 || sink.served != 0 {
		t.Fatalf("aborted %d, lasts %d, served %d", sink.aborted, sink.lasts, sink.served)
	}
	next := corenet.LoopConnRef{Slot: 1, Gen: 2}
	a.OnOpen(io, next)
	a.OnData(io, next, []byte(get("/after")))
	a.OnBatchEnd(io)
	if sink.served != 1 || sink.chunks != 1 {
		t.Fatalf("the slot's next connection: served %d, chunks %d", sink.served, sink.chunks)
	}
}

// TestBodyReader_closeAnswerStopsReading pins the ordinary path behind the
// stream: an answer that closes the connection discards what follows it.
func TestBodyReader_closeAnswerStopsReading(t *testing.T) {
	t.Parallel()
	sink := &bodySink{closeResp: true}
	a := http1.HandleLoop(sink, http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	in := []byte(post("/up", 3) + "abc" + get("/never"))
	if n := a.OnData(io, ref, in); n != len(in) {
		t.Fatalf("took %d of %d", n, len(in))
	}
	if sink.served != 1 {
		t.Fatalf("served %d past a closing answer", sink.served)
	}
}
