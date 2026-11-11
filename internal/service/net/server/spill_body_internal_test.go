package server

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// spillH answers every request 200 with an empty body.
type spillH struct{}

func (spillH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Status = http.StatusOK
}

// TestHTTP1Loop_bodyPastTheSpillIsRefusedOnTheHead pins #844: a request whose
// Content-Length a default loop group's spill cannot hold is answered 413 on
// its head, on every backend, and the connection closed: the client reads a
// status line, never a bare reset after sending up to 64 KiB.
func TestHTTP1Loop_bodyPastTheSpillIsRefusedOnTheHead(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			_, addr := startLoopServer(t, b, http1.HandleLoop(spillH{}, http1.Parser{}), Plaintext())
			c := loopDial(t, addr)
			head := "POST /big HTTP/1.1\r\nHost: h\r\nContent-Length: 100000\r\n\r\n"
			if _, err := c.Write([]byte(head)); err != nil {
				t.Fatal(err)
			}
			// The body the client is still sending: a refusal on the head
			// never waits for it.
			if _, err := c.Write(bytes.Repeat([]byte{'x'}, 1<<16)); err != nil {
				t.Logf("writing the body: %v (the server refused and closed)", err)
			}
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			rd := bufio.NewReader(c)
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("no status line: %v (a bare reset)", err)
			}
			if want := "HTTP/1.1 413"; len(line) < len(want) || line[:len(want)] != want {
				t.Fatalf("status line = %q, want 413", line)
			}
		})
	}
}

// TestHTTP1Loop_explicitBodyPastTheSpillFailsStart pins the second half of #844:
// an explicit Body bound the spill cannot hold is refused at Start naming body,
// on the group that asked for it, before any address is bound.
func TestHTTP1Loop_explicitBodyPastTheSpillFailsStart(t *testing.T) {
	p, err := http1.NewParser(corenet.HTTP1LimitsValue{Body: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	srv := New()
	srv.Group("api", Listen("tcp", "127.0.0.1:0"), Plaintext(), Loop(corenet.LoopValue{})).HandleLoop(http1.HandleLoop(spillH{}, p))
	err = srv.Start(t.Context())
	swallowErr(srv.Close())
	if !errs.HasCode(err, corenet.CodeHTTP1Misconfigured) {
		t.Fatalf("Start = %v, want HTTP1Misconfigured", err)
	}
	if !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
		return f.Key() == "option" && f.StringValue() == "body"
	}) {
		t.Fatalf("Start = %v, fields %v, want option=body", err, errs.FieldsOf(err))
	}
}

// streamH reads a body as it arrives: the chunks and bytes it was given.
type streamH struct {
	spillH
	bytes, chunks atomic.Int64
}

// OnBody implements corenet.HTTP1BodyReader.
func (h *streamH) OnBody(_ *corenet.HTTP1RequestValue, chunk []byte, _ bool) {
	h.bytes.Add(int64(len(chunk)))
	h.chunks.Add(1)
}

// TestHTTP1Loop_aBodyReaderTakesAMebibyteOnTheDefaultSpill pins the second
// acceptance of #844: a handler reading its body as it arrives is given a 1 MiB
// body on a group with the default SpillCap, on every backend, the spill holding
// only the head, and the request is served once the last chunk has been given.
func TestHTTP1Loop_aBodyReaderTakesAMebibyteOnTheDefaultSpill(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := &streamH{}
			_, addr := startLoopServer(t, b, http1.HandleLoop(h, http1.Parser{}), Plaintext())
			c := loopDial(t, addr)
			const size = 1 << 20
			head := fmt.Sprintf("POST /up HTTP/1.1\r\nHost: h\r\nContent-Length: %d\r\n\r\n", size)
			if _, err := c.Write([]byte(head)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Write(bytes.Repeat([]byte{'x'}, size)); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				t.Fatalf("no answer: %v", err)
			}
			if want := "HTTP/1.1 200"; len(line) < len(want) || line[:len(want)] != want {
				t.Fatalf("status line = %q, want 200", line)
			}
			if got := h.bytes.Load(); got != size {
				t.Fatalf("OnBody was given %d bytes, want %d", got, size)
			}
			if h.chunks.Load() < 2 {
				t.Fatalf("OnBody took %d chunks, want the body in pieces", h.chunks.Load())
			}
		})
	}
}
