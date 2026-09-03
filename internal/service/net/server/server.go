// Package server is the inbound half of the SDK's network domain (ADR 0029):
// one unified listener engine for TCP, Unix, TLS and mutual TLS, serving
// pluggable handlers grouped behind shared middlewares and policies.
package server

import (
	"context"
	stdnet "net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

const (
	// defaultDrainTimeout bounds Shutdown when the caller sets no budget.
	defaultDrainTimeout time.Duration = 15 * time.Second
	// expectedGroups pre-sizes the group table. Servers declare a handful of
	// groups, not hundreds; the hint avoids a rehash on the common shapes.
	expectedGroups int = 8
	// expectedLiveConns pre-sizes the live-socket registry so a burst of
	// accepts does not rehash it on the accept path.
	expectedLiveConns int = 256
)

// Server owns a set of listener groups and their lifecycle.
//
// It is safe for concurrent use. Declaration-time mistakes — a duplicate group
// name, an unusable address — are recorded and returned by Start rather than
// panicking or forcing an error check on every declaration, which is what keeps
// wiring a server down to a handful of readable lines.
type Server struct {
	// mu guards the declaration-time fields below.
	mu sync.Mutex
	// groups preserves declaration order so listeners bind predictably.
	groups []*StreamGroup
	// byName rejects a duplicate group name at declaration time.
	byName map[string]*StreamGroup
	// declErr is the first declaration error, surfaced by Start.
	declErr error
	// listeners are the live bound listeners.
	listeners []*boundListener
	// states mirrors listeners for reporting, including any degradation.
	states []corenet.ListenerStateValue
	// drainTimeout bounds Shutdown.
	drainTimeout time.Duration

	// phase is the lifecycle phase, read without the mutex by State.
	phase atomic.Uint32
	// inFlight tracks connections being served, for the drain.
	inFlight sync.WaitGroup
	// active, total and rejected feed State.
	active   atomic.Int64
	total    atomic.Uint64
	rejected atomic.Uint64
	// connPool recycles the per-connection struct so a steady-state accept
	// loop allocates nothing per connection.
	connPool *recycler.Pool[*conn]
	// nextID assigns each connection its correlation identifier.
	nextID atomic.Uint64
	// liveMu guards live.
	liveMu sync.RWMutex
	// live tracks the sockets currently being served, so an expired drain
	// budget can sever them. Without this registry a stuck handler would make
	// Shutdown block forever, which is precisely what a budget exists to
	// prevent.
	live map[uint64]stdnet.Conn
	// stop cancels every in-flight handler when the server shuts down.
	stop context.CancelFunc
	// runCtx is handed to every handler. It deliberately outlives the caller's
	// start context so cancelling that context does not tear handlers down
	// before the drain has had its budget.
	runCtx context.Context
}

// newPooledConn mints an empty connection wrapper for the recycler.
func newPooledConn() *conn {
	//: the pool fills every field in acquire, so a zero value is correct here.
	return &conn{}
}

// New builds a server.
func New(opts ...Option) *Server {
	s := &Server{
		byName:       make(map[string]*StreamGroup, expectedGroups),
		live:         make(map[uint64]stdnet.Conn, expectedLiveConns),
		drainTimeout: defaultDrainTimeout,
		connPool:     recycler.NewPool(newPooledConn),
	}
	//: options apply in order, so a later one deliberately wins.
	for _, opt := range opts {
		opt(s)
	}
	//: a server that is never started still hands handlers a usable context.
	s.runCtx = context.Background()
	//: a freshly built server has bound nothing yet.
	s.phase.Store(uint32(corenet.PhaseNew))
	//: ready to declare groups on.
	return s
}

// Group declares a listener group and returns it for handler attachment.
//
// It returns the group rather than (group, error) on purpose: a declaration
// error is recorded and surfaced by Start, so the common path stays a single
// chained expression instead of an error check per line.
func (s *Server) Group(name string, opts ...GroupOption) *StreamGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a duplicate name would make logs, metrics and State ambiguous.
	if _, exists := s.byName[name]; exists {
		s.recordDeclError(errs.Wrap(corenet.GroupDuplicate, errs.WrapParams{},
			errs.String("group", name)))
		//: hand back a detached group so the caller's chained calls stay safe.
		//: a detached group keeps the caller's chained calls safe.
		return &StreamGroup{name: name}
	}
	group := &StreamGroup{name: name}
	//: options apply in order.
	for _, opt := range opts {
		opt(group)
	}
	s.byName[name] = group
	s.groups = append(s.groups, group)
	//: returned so the caller can attach a handler in the same expression.
	return group
}

// recordDeclError keeps the first declaration error. The caller holds s.mu.
func (s *Server) recordDeclError(err error) {
	//: the first error is the one that explains the others.
	if s.declErr == nil {
		s.declErr = err
	}
}

// State returns a snapshot of the server's lifecycle and listeners.
func (s *Server) State() corenet.StateValue {
	s.mu.Lock()
	listeners := slices.Clone(s.states)
	s.mu.Unlock()
	//: a copied slice so the caller cannot observe a listener set mutating
	//: underneath it, and cannot mutate ours.
	return corenet.StateValue{
		Phase:         corenet.Phase(s.phase.Load()),
		Listeners:     listeners,
		ActiveConns:   s.active.Load(),
		TotalConns:    s.total.Load(),
		RejectedConns: s.rejected.Load(),
	}
}
