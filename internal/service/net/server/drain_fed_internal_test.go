// Package server — a connection fed through the whole drain gets every
// answer to what the server read before it closes (#1385): HTTP/1.1, h2c and
// a raw LoopHandler without OnDrain, on every backend.
package server

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// fedWindow is how many requests a fed connection keeps unanswered: enough
// that every batch of the drain finds bytes on it, few enough that no buffer
// fills and h2c's streams stay within any limit.
const fedWindow = 8

// batchLineH is a raw LoopHandler with no OnDrain that answers each line at
// OnBatchEnd, as the HTTP adapters answer their requests: the case the drain
// lost an answer of before it looked after OnBatchEnd.
type batchLineH struct {
	mu      sync.Mutex
	pending map[corenet.LoopIO][]corenet.LoopConnRef
	// read counts the lines the handler consumed; an answer a write
	// refused is one the client never reads, which the comparison counts.
	read atomic.Int64
	// answer is what each line is answered with, a line itself.
	answer []byte
}

func (h *batchLineH) OnOpen(corenet.LoopIO, corenet.LoopConnRef) {}

func (h *batchLineH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	n := 0
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, b := range buf {
		if b == '\n' {
			h.pending[io] = append(h.pending[io], ref)
			h.read.Add(1)
			n = i + 1
		}
	}
	return n
}

func (h *batchLineH) OnBatchEnd(io corenet.LoopIO) {
	h.mu.Lock()
	refs := h.pending[io]
	h.pending[io] = refs[:0]
	h.mu.Unlock()
	for _, ref := range refs {
		_ = io.Write(ref, h.answer)
	}
}

func (h *batchLineH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)      {}
func (h *batchLineH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)     {}
func (h *batchLineH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) {}

// TestDrain_aConnectionFedThroughTheDrainGetsEveryAnswer keeps one
// connection fed through the whole drain — fedWindow requests always
// unanswered, a new one written as each answer comes —, so every batch finds
// bytes on it, and requires the server's answer to each request it read to
// reach the client before the connection ends, and the drain to end. On
// HTTP/1.1 (a LoopDrainer), h2c and a raw handler with no OnDrain, all three
// answering at OnBatchEnd, on every backend; and the raw handler's large
// answers to a client that reads slowly, still the kernel's when the drain
// closes while the client's requests wait unread: the close lingers, or its
// reset would discard them. The drain used to look at the
// connections before OnBatchEnd: a connection the batch fed was deferred at
// every look, and the drain never ended while traffic lasted; bounded to one
// deferral, it was marked closing before its answer was written, and the
// write was refused.
func TestDrain_aConnectionFedThroughTheDrainGetsEveryAnswer(t *testing.T) {
	type tc struct {
		name string
		// group declares the group on srv and returns how many requests the
		// server read so far.
		group func(srv *Server, b corenet.LoopBackend) func() int64
		// feed writes requests on c, one per slot taken from window, until
		// stop closes or gone says the server refuses more; read reads the
		// answers off c, freeing a slot of window for each, until the
		// connection ends, and returns how many it read.
		feed func(c stdnet.Conn, window chan struct{}, stop <-chan struct{}, gone *atomic.Bool)
		read func(c stdnet.Conn, window chan struct{}, gone *atomic.Bool) int64
		// window is how many requests stay unanswered, fedWindow when zero.
		window int
	}
	hello := func(read *atomic.Int64) corenet.HTTP1HandlerFunc {
		return func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
			read.Add(1)
			res.Body = []byte("ok")
		}
	}
	// feedWith writes request(i) for i = 0, 1, … as feed does.
	feedWith := func(c stdnet.Conn, window chan struct{}, stop <-chan struct{}, gone *atomic.Bool, request func(i int) []byte) {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case window <- struct{}{}:
			}
			if gone.Load() {
				return
			}
			if _, err := c.Write(request(i)); err != nil {
				return
			}
		}
	}
	rawGroup := func(answer []byte, opts ...GroupOption) func(srv *Server, b corenet.LoopBackend) func() int64 {
		return func(srv *Server, b corenet.LoopBackend) func() int64 {
			h := &batchLineH{pending: map[corenet.LoopIO][]corenet.LoopConnRef{}, answer: answer}
			srv.Group("loop", append([]GroupOption{Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: b})}, opts...)...).HandleLoop(h)
			return h.read.Load
		}
	}
	feedLines := func(c stdnet.Conn, window chan struct{}, stop <-chan struct{}, gone *atomic.Bool) {
		feedWith(c, window, stop, gone, func(int) []byte { return []byte("q\n") })
	}
	// readLines counts each whole line, pausing pause after each.
	readLines := func(pause time.Duration) func(c stdnet.Conn, window chan struct{}, _ *atomic.Bool) int64 {
		return func(c stdnet.Conn, window chan struct{}, _ *atomic.Bool) int64 {
			br := bufio.NewReader(c)
			n := int64(0)
			for {
				if _, err := br.ReadBytes('\n'); err != nil {
					return n
				}
				n++
				time.Sleep(pause)
				<-window
			}
		}
	}
	cases := []tc{
		{
			name:  "raw handler, no OnDrain",
			group: rawGroup([]byte("a\n")),
			feed:  feedLines,
			read:  readLines(0),
		},
		{
			// The answers outrun a client that reads slowly, so the kernel
			// still holds some when the drain closes; the client keeps
			// writing, so the server has input unread: closed then, the
			// connection would reset, and the reset discards the answers
			// the client has not read yet. The close lingers instead.
			//
			// The server's send buffer is small, as the BSDs' are by
			// default, so a close's output leaves through the writable
			// path, a write refused with EAGAIN resumed once the socket has
			// room: on FreeBSD and NetBSD that path never settled a closing
			// connection, and the drain waited for its deadline.
			name:   "raw handler, a slow reader",
			group:  rawGroup(append(bytes.Repeat([]byte("x"), 8191), '\n'), SendBuffer(8<<10)),
			feed:   feedLines,
			read:   readLines(200 * time.Microsecond),
			window: 64,
		},
		{
			name: "http1",
			group: func(srv *Server, b corenet.LoopBackend) func() int64 {
				read := &atomic.Int64{}
				srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Loop(corenet.LoopValue{Backend: b})).HandleLoop(http1.HandleLoop(hello(read), http1.Parser{}))
				return read.Load
			},
			feed: func(c stdnet.Conn, window chan struct{}, stop <-chan struct{}, gone *atomic.Bool) {
				feedWith(c, window, stop, gone, func(int) []byte { return []byte("GET / HTTP/1.1\r\nHost: drain\r\n\r\n") })
			},
			read: func(c stdnet.Conn, window chan struct{}, gone *atomic.Bool) int64 {
				br := bufio.NewReader(c)
				n := int64(0)
				for {
					res, err := http.ReadResponse(br, nil)
					if err != nil {
						return n
					}
					body, err := io.ReadAll(io.LimitReader(res.Body, 16))
					swallowErr(res.Body.Close())
					if err != nil || res.StatusCode != http.StatusOK || string(body) != "ok" {
						return n
					}
					n++
					//: the server's last answer: it reads nothing more.
					if res.Close {
						gone.Store(true)
					}
					<-window
				}
			},
		},
		{
			name: "h2c",
			group: func(srv *Server, b corenet.LoopBackend) func() int64 {
				read := &atomic.Int64{}
				srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Loop(corenet.LoopValue{Backend: b})).HandleLoop(http2.HandleLoop(hello(read), http1.Parser{}))
				return read.Load
			},
			feed: func(c stdnet.Conn, window chan struct{}, stop <-chan struct{}, gone *atomic.Bool) {
				if _, err := c.Write(append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), frameBytes(corehttp2.FrameSettings, 0, 0, nil)...)); err != nil {
					return
				}
				feedWith(c, window, stop, gone, func(i int) []byte {
					return frameBytes(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, uint32(2*i+1), requestBlock("GET", "/"))
				})
			},
			read: func(c stdnet.Conn, window chan struct{}, gone *atomic.Bool) int64 {
				br := bufio.NewReader(c)
				n := int64(0)
				for {
					typ, flags, stream, payload, err := readH2Frame(br)
					if err != nil {
						return n
					}
					switch {
					case typ == corehttp2.FrameSettings && flags&corehttp2.FlagAck == 0:
						if _, err := c.Write(frameBytes(corehttp2.FrameSettings, corehttp2.FlagAck, 0, nil)); err != nil {
							return n
						}
					//: the server takes no stream past it: no more are opened.
					case typ == corehttp2.FrameGoAway && len(payload) >= 8:
						gone.Store(true)
					case stream != 0 && (typ == corehttp2.FrameData || typ == corehttp2.FrameHeaders) && flags&corehttp2.FlagEndStream != 0:
						n++
						<-window
					case typ == corehttp2.FrameRSTStream && len(payload) >= 4:
						//: a stream past the GOAWAY's last is refused: its slot frees.
						if binary.BigEndian.Uint32(payload) == uint32(corehttp2.ErrCodeRefusedStream) {
							<-window
						}
					}
				}
			},
		},
	}
	runCase := func(t *testing.T, c tc, b corenet.LoopBackend) {
		srv := New(WithDrainGrace(10 * time.Millisecond))
		t.Cleanup(func() { swallowErr(srv.Close()) })
		read := c.group(srv, b)
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		conn := loopDial(t, srv.State().Listeners[0].Address)
		window := make(chan struct{}, cmp.Or(c.window, fedWindow))
		stop := make(chan struct{})
		var stopOnce sync.Once
		stopFeed := func() { stopOnce.Do(func() { close(stop) }) }
		var gone atomic.Bool
		fed, readDone := make(chan struct{}), make(chan struct{})
		answered := make(chan int64, 1)
		// Registered before the workers start: a case that fails stops the
		// feeder, which may wait on a full window, and closes the socket
		// the reader waits on, then waits for both.
		t.Cleanup(func() {
			stopFeed()
			swallowErr(conn.Close())
			<-fed
			<-readDone
		})
		go func() {
			defer close(fed)
			c.feed(conn, window, stop, &gone)
		}()
		go func() {
			defer close(readDone)
			answered <- c.read(conn, window, &gone)
		}()
		for deadline := time.Now().Add(5 * time.Second); read() < 50; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the connection was not fed: %d requests read in 5 s", read())
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shut := make(chan error, 1)
		go func() { shut <- srv.Shutdown(ctx) }()
		var got int64
		select {
		case got = <-answered:
		case <-time.After(15 * time.Second):
			t.Fatalf("the connection fed through the drain never ended: %d requests read", read())
		}
		// The client has read the end: it closes, as a client does, which
		// ends the server's lingering close.
		swallowErr(conn.Close())
		stopFeed()
		<-fed
		if err := <-shut; err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown: %v", err)
		} else if err != nil {
			t.Fatalf("the drain did not end within 10 s with the connection fed: %v", err)
		}
		if want := read(); got != want {
			t.Fatalf("the client read %d answers, the server read %d requests: %d answers lost", got, want, want-got)
		}
		t.Logf("%d requests read, every one answered", got)
	}
	for _, c := range cases {
		for _, b := range loopBackends() {
			t.Run(c.name+"/"+b.String(), func(t *testing.T) { runCase(t, c, b) })
		}
	}
}
