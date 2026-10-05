package server

import (
	"context"
	stdnet "net"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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

// newPooledConn mints an empty connection wrapper for the recycler.
func newPooledConn() *conn {
	//: the pool fills every field in acquire, so a zero value is correct here.
	return &conn{}
}

// New builds a server.
func New(opts ...Option) *Server {
	s := &Server{
		byName:       make(map[string]*StreamGroup, expectedGroups),
		packetByName: make(map[string]*PacketGroup, expectedGroups),
		live:         make(map[uint64]stdnet.Conn, expectedLiveConns),
		drainTimeout: defaultDrainTimeout,
		connPool:     recycler.NewPool(newPooledConn),
		clk:          clock.System,
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

// PacketGroup declares a datagram listener group and returns it for handler
// attachment. It mirrors Group deliberately: the point of the domain is that a
// UDP service is declared the same way a TCP one is.
func (s *Server) PacketGroup(name string, opts ...GroupOption) *PacketGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: names are shared across both natures, so one group cannot shadow another
	//: in logs, metrics or State.
	_, streamTaken := s.byName[name]
	_, packetTaken := s.packetByName[name]
	//: a name shared with either nature would make logs and State ambiguous.
	if streamTaken || packetTaken {
		s.recordDeclError(errs.Wrap(corenet.GroupDuplicate, errs.WrapParams{},
			errs.String("group", name)))
		//: a detached group keeps the caller's chained calls safe.
		return &PacketGroup{name: name}
	}
	group := &PacketGroup{name: name}
	shim := &StreamGroup{}
	//: the option set is shared, so apply it to a shim and copy what applies.
	for _, opt := range opts {
		opt(shim)
	}
	group.addrs = shim.addrs
	group.limits = shim.limits
	group.adopt = shim.adopt
	s.packetByName[name] = group
	s.packetGroups = append(s.packetGroups, group)
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
	s.mu.RLock()
	listeners := slices.Clone(s.states)
	s.mu.RUnlock()
	//: a copied slice so the caller cannot observe a listener set mutating
	//: underneath it, and cannot mutate ours.
	return corenet.StateValue{
		Phase:            corenet.Phase(s.phase.Load()),
		Listeners:        listeners,
		ActiveConns:      s.active.Load(),
		TotalConns:       s.total.Load(),
		RejectedConns:    s.rejected.Load(),
		OversizedPackets: s.oversized.Load(),
		AcceptBackoffs:   s.acceptBackoffs.Load(),
	}
}
