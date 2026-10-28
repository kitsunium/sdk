package server

import (
	"context"
	stdnet "net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
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

// lookupFunc reads one environment variable: os.LookupEnv, or a test's map.
type lookupFunc func(key string) (string, bool)

// Server owns a set of listener groups and their lifecycle.
//
// It is safe for concurrent use. Declaration-time mistakes — a duplicate group
// name, an unusable address — are recorded and returned by Start rather than
// panicking or forcing an error check on every declaration, which is what keeps
// wiring a server down to a handful of readable lines.
type Server struct {
	// mu guards the declaration-time fields below.
	mu sync.RWMutex
	// groups preserves declaration order so listeners bind predictably.
	groups []*StreamGroup
	// packetGroups preserves declaration order for the datagram side.
	packetGroups []*PacketGroup
	// packetByName rejects a duplicate datagram group name.
	packetByName map[string]*PacketGroup
	// packetConns are the live bound datagram sockets.
	packetConns []*boundPacketConn
	// byName rejects a duplicate group name at declaration time.
	byName map[string]*StreamGroup
	// declErr is the first declaration error, surfaced by Start.
	declErr error
	// listeners are the live bound listeners.
	listeners []*boundListener
	// loopEngines are the running loop groups (ADR 0186).
	loopEngines []loopEngine
	// states mirrors listeners for reporting, including any degradation.
	states []corenet.ListenerStateValue
	// drainTimeout bounds Shutdown.
	drainTimeout time.Duration
	// drainGrace is how long Shutdown keeps serving, readiness false, before
	// it closes a listener.
	drainGrace time.Duration

	// phase is the lifecycle phase, read without the mutex by State.
	phase atomic.Uint32
	// inFlight tracks connections being served, for the drain.
	inFlight sync.WaitGroup
	// active, total, rejected and oversized feed State.
	active   atomic.Int64
	total    atomic.Uint64
	rejected atomic.Uint64
	// oversized counts datagrams dropped for passing their group's ceiling. A
	// read loop has nobody to hand an error to, so this counter is how the drop
	// is reported instead of hidden.
	oversized atomic.Uint64
	// datagrams counts what the batched datagram paths drop (ADR 0192).
	datagrams datagramCounters
	// gsoMu guards gsoOff.
	gsoMu sync.Mutex
	// gsoOff names, once each, the groups a socket of which the device refused
	// GSO for: a fault State lists for as long as the server lives.
	gsoOff []string
	// acceptBackoffs counts the waits accept loops took after a failed Accept —
	// descriptor exhaustion, typically — so a server that is failing to accept
	// says so in State instead of spinning a core per listener.
	acceptBackoffs atomic.Uint64
	// clk is the engine's clock: what an accept loop's backoff waits on.
	clk clock.Timed
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
	// inheritOnce reads the supervisor's socket table at most once per server.
	inheritOnce sync.Once
	// inherited is that table, held for the server's lifetime so no wrapper is
	// ever collected and no finaliser closes a descriptor still to be adopted.
	inherited map[string][]*os.File
	// inheritErr is the failure to read it, reported to every adoption.
	inheritErr error
	// stop cancels every in-flight handler when the server shuts down.
	stop context.CancelFunc
	// runCtx is handed to every handler. It deliberately outlives the caller's
	// start context so cancelling that context does not tear handlers down
	// before the drain has had its budget.
	runCtx context.Context
	// lookup reads the operator's variables; os.LookupEnv unless a test says.
	lookup lookupFunc
	// operator is what they said, read once by New, before any probe.
	operator operatorEnv
	// platform is what State names the server runs on, read once by New.
	platform corenet.PlatformValue
	// fallbackMu guards fallbacks.
	fallbackMu sync.Mutex
	// fallbacks counts the engine fallbacks and keeps the last one.
	fallbacks corenet.FallbacksValue
	// stall watches the loop groups for one every ring of which is drained.
	stall stallWatch
}

// newPooledConn mints an empty connection wrapper for the recycler.
func newPooledConn() *conn {
	//: the pool fills every field in acquire, so a zero value is correct here.
	return &conn{}
}

// New builds a server.
func New(opts ...Option) *Server {
	//: the operator's variables come from the process environment.
	return newServer(os.LookupEnv, opts...)
}

// newServer builds a server whose operator's variables are read through
// lookup: os.LookupEnv for New, a map for a test.
func newServer(lookup lookupFunc, opts ...Option) *Server {
	s := &Server{
		byName:       make(map[string]*StreamGroup, expectedGroups),
		packetByName: make(map[string]*PacketGroup, expectedGroups),
		live:         make(map[uint64]stdnet.Conn, expectedLiveConns),
		drainTimeout: defaultDrainTimeout,
		connPool:     recycler.NewPool(newPooledConn),
		clk:          clock.System,
		lookup:       lookup,
		stall:        stallWatch{stalled: make(chan struct{})},
	}
	//: options apply in order, so a later one deliberately wins.
	for _, opt := range opts {
		opt(s)
	}
	//: the operator's switches, read once and before any probe (ADR 0188).
	s.operator = readOperator(s.lookup)
	s.platform = readPlatform()
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
	group := &StreamGroup{name: name}
	//: options apply in order, before the name is checked: Name may set it.
	//: They touch only the new group, so they run outside the server's lock.
	for _, opt := range opts {
		opt(group)
	}
	group.reconcileName()
	name = group.name
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a duplicate name would make logs, metrics and State ambiguous.
	if _, exists := s.byName[name]; exists {
		s.recordDeclError(errs.Wrap(corenet.GroupDuplicate, errs.WrapParams{},
			errs.String("group", name)))
		//: a detached group keeps the caller's chained calls safe.
		return &StreamGroup{name: name}
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
	shim := &StreamGroup{name: name}
	//: the option set is shared, so apply it to a shim and copy what applies;
	//: before the name is checked, since Name may set it, and outside the
	//: server's lock, since they touch only the shim.
	for _, opt := range opts {
		opt(shim)
	}
	shim.reconcileName()
	name = shim.name
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
	s.judgePacketOptions(shim)
	group.addrs = shim.addrs
	group.limits = shim.limits
	group.adopt = shim.adopt
	group.batchReplies = shim.kinds.has(corenet.OptionBatchReplies)
	s.packetByName[name] = group
	s.packetGroups = append(s.packetGroups, group)
	//: returned so the caller can attach a handler in the same expression.
	return group
}

// judgePacketOptions records the first option a datagram group cannot honour
// (ADR 0186 §D1, ADR 0187), for Start to report: a Name that contradicts the
// declared one, Loop, which has no datagram engine, and Plaintext with TLS or
// an Engine other than the goroutine one. The caller holds s.mu.
func (s *Server) judgePacketOptions(shim *StreamGroup) {
	//: the declared name and Name disagree.
	if shim.declErr != nil {
		s.recordDeclError(shim.declErr)
	}
	//: a datagram group has no loop engine: the option is refused at Start
	//: rather than dropped with the rest of the shim.
	if shim.loopSet {
		s.recordDeclError(errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", shim.name), errs.String("option", "Loop")))
	}
	//: judged as a goroutine group's: only EngineGoroutine or EngineAuto serve it.
	if err := shim.engineRefusal(); err != nil {
		s.recordDeclError(err)
	}
	//: a datagram group serves no HTTP: Protocols is refused at Start rather
	//: than dropped with the rest of the shim (ADR 0195).
	if err := shim.protocolsRefusal(); err != nil {
		s.recordDeclError(err)
	}
	//: every other kind the datagram engine does not apply (ADR 0187 §D4).
	for _, k := range shim.kinds.list() {
		//: the first one is refused, never dropped (R6).
		if !packetHonours(k) {
			s.recordDeclError(errs.Wrap(corenet.PacketOptionUnsupported, errs.WrapParams{},
				errs.String("group", shim.name), errs.String("option", k.String())))
			//: one is enough.
			return
		}
	}
}

// packetHonours reports whether a datagram group applies the option of kind
// k: its ceilings, its batched replies (ADR 0192), adoption, and the three
// every engine honours. Engine and Plaintext are judged by engineRefusal.
func packetHonours(k corenet.OptionKind) bool {
	//: what PacketGroup keeps of its shim.
	switch k {
	case corenet.OptionMaxPacketSize, corenet.OptionBatchSize, corenet.OptionBatchReplies, corenet.OptionAdopt,
		corenet.OptionName, corenet.OptionEngine, corenet.OptionPlaintext:
		//: honoured.
		return true
	default:
		//: dropped with the shim: refused.
		return false
	}
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
	s.fallbackMu.Lock()
	fallbacks := s.fallbacks
	s.fallbackMu.Unlock()
	override := s.operator.override
	override.Disabled = slices.Clone(override.Disabled)
	//: a copied slice so the caller cannot observe a listener set mutating
	//: underneath it, and cannot mutate ours.
	st := corenet.StateValue{
		Phase:            corenet.Phase(s.phase.Load()),
		Listeners:        listeners,
		ActiveConns:      s.active.Load(),
		TotalConns:       s.total.Load(),
		RejectedConns:    s.rejected.Load(),
		OversizedPackets: s.oversized.Load(),
		AcceptBackoffs:   s.acceptBackoffs.Load(),
		Datagrams: corenet.DatagramStateValue{
			TruncatedPackets: s.datagrams.truncated.Load(),
			GSOOversized:     s.datagrams.gsoOversized.Load(),
			ReplyFailures:    s.datagrams.replyFailures.Load(),
		},
		Loops:     s.loopStates(),
		Override:  override,
		Fallbacks: fallbacks,
		Platform:  s.platform,
	}
	st.Faults = s.faults(st)
	//: a snapshot.
	return st
}

// recordFallback counts a group started on a lower engine than its code asked
// for, because the better one failed, and keeps it as the last (ADR 0188).
func (s *Server) recordFallback(group, reason string) {
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	s.fallbacks.Count++
	s.fallbacks.LastGroup = group
	s.fallbacks.LastReason = reason
	s.fallbacks.LastAt = s.clk.Now()
}
