package rpc

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// ServerConfig bounds what one connection of a server may cost. Every bound
// is the caller's decision: none has a default, and one that is not positive
// is refused (ADR 0031).
type ServerConfig struct {
	// Formats are the body formats the server speaks, by the name a codec is
	// registered under (core/data/codec): a client's hello names one of them.
	Formats []codec.Format
	// MaxFrameBytes is the longest frame the server reads — its kind, id,
	// method and body — checked on the length before anything is read.
	MaxFrameBytes int
	// MaxInFlight is how many calls run at once on one connection; the next is
	// OVERLOADED.
	MaxInFlight int
	// FrameTimeout is how long a frame may take, from its first byte to its
	// last, read or written; past it the connection is closed.
	FrameTimeout time.Duration
	// Clock times the frames and the calls' budgets; nil is clock.System.
	Clock clock.Timed
}

// Server answers the calls of every connection it is handed, through its
// Router, within the bounds of its ServerConfig.
type Server struct {
	// cfg is the validated configuration, its formats in codecs.
	cfg ServerConfig
	// codecs is each format's codec, looked up once by NewServer.
	codecs map[codec.Format]codec.Codec
	// router names each method's handler.
	router corerpc.Router
	// clk is the injected clock.
	clk clock.Timed
	// inflight counts the handlers running, on every connection.
	inflight atomic.Int64
	// ctx ends with Close; every connection's context derives from it.
	ctx context.Context
	// cancel ends ctx.
	cancel context.CancelCauseFunc
	// mu guards closed and lns.
	mu sync.Mutex
	// closed is set by Close.
	closed bool
	// lns are the listeners Serve accepts on, which Close closes.
	lns map[coreipc.Listener]struct{}
}

// NewServer validates cfg and returns a server routing calls through r. A
// bound that is not positive, no format, a format no codec is registered
// under, or a nil r is MISCONFIGURED.
func NewServer(cfg ServerConfig, r corerpc.Router) (*Server, error) {
	codecs, err := serverCodecs(cfg)
	//: a configuration no connection can run with.
	if err != nil {
		return nil, err
	}
	//: no router answers nothing.
	if r == nil {
		return nil, misconfigured("no Router")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Server{
		cfg: cfg, codecs: codecs, router: r, clk: clockOr(cfg.Clock),
		ctx: ctx, cancel: cancel, lns: make(map[coreipc.Listener]struct{}),
	}, nil
}

// Serve accepts ln's connections and serves each in a goroutine until ln is
// closed, or the server is, then waits for those goroutines: it returns nil
// after Close, the listener's error otherwise.
//
// Goroutine lifecycle: one per accepted connection, running ServeConn; each
// ends with its connection — the peer's hang-up, a violation, or Close,
// which ends them all — and Serve returns only once every one has.
func (s *Server) Serve(ln coreipc.Listener) error {
	//: a closed server accepts nothing.
	if !s.track(ln) {
		return nil
	}
	defer s.untrack(ln)
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		c, err := ln.Accept()
		//: the listener closed: by Close, or under the server.
		if err != nil {
			return s.acceptErr(err)
		}
		conns.Add(1)
		go s.serveAccepted(&conns, c)
	}
}

// serveAccepted serves one accepted connection, then tells Serve, which waits
// for it; its verdict was the peer's to read.
func (s *Server) serveAccepted(conns *sync.WaitGroup, c *coreipc.Conn) {
	defer conns.Done()
	swallowErr(s.ServeConn(s.ctx, c))
}

// acceptErr is nil when the server was closed, the listener's error
// otherwise.
func (s *Server) acceptErr(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: Close closed the listener.
	if s.closed {
		return nil
	}
	return err
}

// track records ln for Close, and false when the server is already closed.
func (s *Server) track(ln coreipc.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: Close came first.
	if s.closed {
		return false
	}
	s.lns[ln] = struct{}{}
	return true
}

// untrack forgets ln.
func (s *Server) untrack(ln coreipc.Listener) {
	s.mu.Lock()
	delete(s.lns, ln)
	s.mu.Unlock()
}

// ServeConn serves one connection until it ends, ctx ends or the server is
// closed, closes it, and returns once its handlers have — every handler's
// context ends with the connection. A peer that broke the protocol is the
// error, so is a frame past MaxFrameBytes; a peer that hung up is nil.
func (s *Server) ServeConn(ctx context.Context, c *coreipc.Conn) error {
	sc := newServerConn(s, c)
	stopServer := context.AfterFunc(s.ctx, func() { sc.teardown(context.Cause(s.ctx)) })
	defer stopServer()
	stopCaller := context.AfterFunc(ctx, func() { sc.teardown(connClosed()) })
	defer stopCaller()
	return sc.serve()
}

// InFlight is how many handlers run now, on every connection.
func (s *Server) InFlight() int {
	return int(s.inflight.Load())
}

// Close stops Serve, closes every connection and ends every handler's
// context. It is idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	//: a second Close has nothing left to do.
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	lns := slices.Collect(maps.Keys(s.lns))
	s.mu.Unlock()
	s.cancel(connClosed())
	var first error
	for _, ln := range lns {
		//: the first listener's error is the one returned; all are closed.
		if err := ln.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// swallowErr drops an error nobody can act on: a connection served detached
// ended, and its verdict was the peer's to read. It is an explicit, reviewed
// no-op rather than a bare `_ =`.
func swallowErr(err error) {
	//: read the parameter so the discard is a decision, not an oversight.
	if err == nil {
		return
	}
}
