// Package server — loop groups, reached through the unexported handleLoop
// until HandleLoop is published (ADR 0186 §D9 step 11).
package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// loopLineH echoes newline-terminated lines and answers "BIG n" with n
// pattern bytes, queuing what Write refuses for OnWritable. It consumes only
// whole lines, so a line split across reads goes through the spill.
type loopLineH struct {
	mu      sync.Mutex
	opens   int
	closes  []error
	queue   map[corenet.LoopConnRef][]byte
	honours []corenet.OptionKind
}

func (h *loopLineH) OnOpen(corenet.LoopIO, corenet.LoopConnRef) {
	h.mu.Lock()
	h.opens++
	h.mu.Unlock()
}

func (h *loopLineH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	used := 0
	for {
		i := bytes.IndexByte(buf[used:], '\n')
		if i < 0 {
			return used
		}
		line := buf[used : used+i+1]
		switch {
		case bytes.HasPrefix(line, []byte("BIG ")):
			n, _ := strconv.Atoi(string(bytes.TrimSpace(line[4:])))
			h.push(ref, loopPattern(n))
		case bytes.Equal(line, []byte("CLOSE\n")):
			_ = io.Close(ref)
		default:
			h.push(ref, line)
		}
		h.OnWritable(io, ref)
		used += i + 1
	}
}

func (h *loopLineH) push(ref corenet.LoopConnRef, p []byte) {
	h.mu.Lock()
	if h.queue == nil {
		h.queue = map[corenet.LoopConnRef][]byte{}
	}
	h.queue[ref] = append(h.queue[ref], p...)
	h.mu.Unlock()
}

func (h *loopLineH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef) {}

func (h *loopLineH) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rest := h.queue[ref]
	for len(rest) > 0 {
		n := min(len(rest), 32<<10)
		if io.Write(ref, rest[:n]) != nil {
			break
		}
		rest = rest[n:]
	}
	if len(rest) == 0 {
		delete(h.queue, ref)
	} else {
		h.queue[ref] = rest
	}
}

func (h *loopLineH) OnBatchEnd(corenet.LoopIO) {}

func (h *loopLineH) OnClose(_ corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	h.mu.Lock()
	h.closes = append(h.closes, err)
	delete(h.queue, ref)
	h.mu.Unlock()
}

func (h *loopLineH) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.opens, len(h.closes)
}

// honouringH is a loopLineH that declares the options it applies itself.
type honouringH struct{ loopLineH }

func (h *honouringH) Honours(k corenet.OptionKind) bool { return slices.Contains(h.honours, k) }

// loopPattern is n bytes counting 0 to 250.
func loopPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// loopBackends are the backends this platform serves a loop group on.
func loopBackends() []corenet.LoopBackend {
	if runtime.GOOS == "linux" {
		return []corenet.LoopBackend{corenet.LoopBackendAuto, corenet.LoopBackendEpoll, corenet.LoopBackendPortable}
	}
	return []corenet.LoopBackend{corenet.LoopBackendAuto, corenet.LoopBackendPortable}
}

// startLoopServer starts a server with one loop group on b.
func startLoopServer(t *testing.T, b corenet.LoopBackend, h corenet.LoopHandler, opts ...GroupOption) (*Server, string) {
	t.Helper()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	opts = append([]GroupOption{Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: b, OutputCap: 128 << 10})}, opts...)
	srv.Group("loop", opts...).handleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return srv, srv.State().Listeners[0].Address
}

// loopDial dials addr with a deadline.
func loopDial(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	t.Cleanup(func() { swallowErr(c.Close()) })
	return c
}

// TestLoopGroup_serves runs the contract on every backend the platform has:
// split lines come back whole and in order, a response far past the socket
// buffer arrives byte-exact with the echo queued behind it, and State names
// the backend.
func TestLoopGroup_serves(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := &loopLineH{}
			srv, addr := startLoopServer(t, b, h)
			c := loopDial(t, addr)
			msg := "one\ntwo\n"
			for i := range len(msg) {
				_, _ = c.Write([]byte{msg[i]})
			}
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
				t.Fatalf("echo = %q, %v", got, err)
			}
			const n = 4 << 20
			_, _ = c.Write([]byte("BIG " + strconv.Itoa(n) + "\nafter\n"))
			r := bufio.NewReader(c)
			big := make([]byte, n)
			if _, err := io.ReadFull(r, big); err != nil || !bytes.Equal(big, loopPattern(n)) {
				t.Fatalf("large response altered: %v", err)
			}
			if line, err := r.ReadString('\n'); err != nil || line != "after\n" {
				t.Fatalf("after = %q, %v", line, err)
			}
			st := srv.State()
			if len(st.Loops) != 1 {
				t.Fatalf("State.Loops = %+v", st.Loops)
			}
			want := b
			if b == corenet.LoopBackendAuto {
				want = corenet.LoopBackendEpoll
				if runtime.GOOS != "linux" {
					want = corenet.LoopBackendPortable
				}
			}
			if st.Loops[0].Backend != want || st.Loops[0].Degraded {
				t.Fatalf("backend %v degraded %v, want %v", st.Loops[0].Backend, st.Loops[0].Degraded, want)
			}
		})
	}
}

// TestLoopGroup_closeAndSpill pins Close after the output left, and the spill
// ceiling, on every backend.
func TestLoopGroup_closeAndSpill(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := &loopLineH{}
			_, addr := startLoopServer(t, b, h, Loop(corenet.LoopValue{Backend: b, SpillCap: 4096}))
			c := loopDial(t, addr)
			_, _ = c.Write([]byte("bye\nCLOSE\n"))
			got, err := io.ReadAll(c)
			if err != nil || string(got) != "bye\n" {
				t.Fatalf("before the close = %q, %v", got, err)
			}
			c2 := loopDial(t, addr)
			_, _ = c2.Write(bytes.Repeat([]byte{'x'}, 32<<10))
			deadline := time.Now().Add(5 * time.Second)
			for {
				h.mu.Lock()
				found := slices.ContainsFunc(h.closes, func(e error) bool { return errors.Is(e, corenet.LoopSpillFull) })
				h.mu.Unlock()
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no LoopSpillFull close: %v", h.closes)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// TestLoopGroup_shutdown pins the drain: Shutdown from another goroutine
// closes every connection, OnClose for each, and returns.
func TestLoopGroup_shutdown(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			h := &loopLineH{}
			srv, addr := startLoopServer(t, b, h)
			for range 8 {
				c := loopDial(t, addr)
				_, _ = c.Write([]byte("hi\n"))
				_, _ = io.ReadFull(c, make([]byte, 3))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- srv.Shutdown(ctx) }()
			if err := <-errc; err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if o, cl := h.counts(); o != 8 || cl != 8 {
				t.Fatalf("%d opened, %d closed", o, cl)
			}
		})
	}
}

// TestLoopGroup_refusals pins what a loop group refuses at Start (ADR 0186
// §D1, §D2), and what wins.
func TestLoopGroup_refusals(t *testing.T) {
	t.Parallel()
	id := testIdentity(t)
	type tc struct {
		name    string
		opts    []GroupOption
		h       corenet.LoopHandler
		also    func(g *StreamGroup)
		want    errs.Code
		wantOpt string
	}
	tests := []tc{
		{name: "TLS", opts: []GroupOption{TLS(id)}, want: corenet.CodeLoopTLSUnsupported},
		{name: "Use", also: func(g *StreamGroup) { g.Use(func(next corenet.ConnHandler) corenet.ConnHandler { return next }) }, want: corenet.CodeLoopOptionUnsupported, wantOpt: "Use"},
		{name: "ReadTimeout nobody honours", opts: []GroupOption{ReadTimeout(time.Second)}, want: corenet.CodeLoopOptionUnsupported, wantOpt: "ReadTimeout"},
		{name: "ReadHeaderTimeout nobody honours", opts: []GroupOption{ReadHeaderTimeout(time.Second)}, want: corenet.CodeLoopOptionUnsupported, wantOpt: "ReadHeaderTimeout"},
		{name: "Shards", opts: []GroupOption{Shards(2)}, want: corenet.CodeLoopOptionUnsupported, wantOpt: "Shards"},
		{name: "ReadHeaderTimeout the handler honours", opts: []GroupOption{ReadHeaderTimeout(time.Second)}, h: &honouringH{loopLineH{honours: []corenet.OptionKind{corenet.OptionReadHeaderTimeout}}}},
		{name: "Use even when the handler claims it", h: &honouringH{loopLineH{honours: []corenet.OptionKind{corenet.OptionMiddleware}}}, also: func(g *StreamGroup) { g.Use(func(next corenet.ConnHandler) corenet.ConnHandler { return next }) }, want: corenet.CodeLoopOptionUnsupported, wantOpt: "Use"},
		{name: "IdleTimeout and MaxConns are the engine's", opts: []GroupOption{IdleTimeout(time.Minute), MaxConns(10)}},
		{name: "a second handler wins", also: func(g *StreamGroup) { g.HandleFunc(func(context.Context, corenet.Conn) error { return nil }) }, opts: []GroupOption{TLS(id)}, want: corenet.CodeHandlerAlreadySet},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			h := c.h
			if h == nil {
				h = &loopLineH{}
			}
			g := srv.Group("loop", append([]GroupOption{Listen("tcp", "127.0.0.1:0")}, c.opts...)...).handleLoop(h)
			if c.also != nil {
				c.also(g)
			}
			err := srv.Start(t.Context())
			if c.want == 0 {
				if err != nil {
					t.Fatalf("Start = %v, want served", err)
				}
				return
			}
			if !errs.HasCode(err, c.want) {
				t.Fatalf("Start = %v, want %v", err, c.want)
			}
			if c.wantOpt != "" && !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
				return f.Key() == "option" && f.StringValue() == c.wantOpt
			}) {
				t.Fatalf("fields %v, want option=%s", errs.FieldsOf(err), c.wantOpt)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("a refused Start bound %d listeners", n)
			}
		})
	}
}
