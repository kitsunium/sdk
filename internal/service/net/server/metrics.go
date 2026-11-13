package server

import (
	"reflect"
	"slices"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The names of the instruments RegisterMetrics creates (#847): observables, each read once per collection from State, never per request.
const (
	// MetricConnsActive names the connections a stream group is serving now, a gauge per
	// group (attribute group).
	MetricConnsActive string = "net_server_conns_active"
	// MetricConnsTotal names the connections a stream group served since the server
	// started - accepted, then past a PROXY header and the ceiling -, a counter per
	// group (attribute group): a connection MetricConnsRejected counts is not in it,
	// although State's TotalConns counts it too (#847).
	MetricConnsTotal string = "net_server_conns_total"
	// MetricConnsRejected names the connections a stream group's ceiling turned away, a
	// counter per group (attribute group): State's RejectedConns, split by group.
	MetricConnsRejected string = "net_server_conns_rejected_total"
	// MetricAcceptBackoffs names the waits the accept loops took after a failed Accept:
	// State's AcceptBackoffs.
	MetricAcceptBackoffs string = "net_server_accept_backoffs_total"
	// MetricDatagramsTruncated names the datagrams the batched read cut: State's
	// Datagrams.TruncatedPackets.
	MetricDatagramsTruncated string = "net_server_datagrams_truncated_total"
	// MetricGSOOversized names the GSO sends the device refused: State's
	// Datagrams.GSOOversized.
	MetricGSOOversized string = "net_server_gso_oversized_total"
	// MetricReplyFailures names the replies that did not leave: State's
	// Datagrams.ReplyFailures.
	MetricReplyFailures string = "net_server_datagram_reply_failures_total"
	// MetricSuppressedLogs names the lines of net/http's own log, and the failed TLS
	// handshakes, the server did not write: State's SuppressedLogs.
	MetricSuppressedLogs string = "net_server_suppressed_logs_total"
	// MetricDegraded names 1 while State reports the server degraded - a fallback, a
	// fault, a listener below what it asked for -, 0 otherwise.
	MetricDegraded string = "net_server_degraded"
	// MetricLoopBatches names the batches a loop ring ran, a counter per ring (attributes
	// group, loop, ring).
	MetricLoopBatches string = "net_loop_batches_total"
	// MetricLoopCompletions names the completions a loop ring reaped, a counter per ring
	// (attributes group, loop, ring).
	MetricLoopCompletions string = "net_loop_completions_total"
	// MetricLoopBufferStarved names the receives a loop ring found no provided buffer for,
	// a counter per ring (attributes group, loop, ring).
	MetricLoopBufferStarved string = "net_loop_buffer_starved_total"
	// MetricLoopBatchesCut names the batches a loop ring's budget cut short, a counter per
	// ring (attributes group, loop, ring).
	MetricLoopBatchesCut string = "net_loop_batches_cut_total"
	// MetricLoopRingsDrained names the rings of a loop group the accept watchdog has
	// drained, a gauge per group (attribute group): all of them is a stalled group.
	MetricLoopRingsDrained string = "net_loop_rings_drained"
	// MetricLoopOutboundRefused names the offloaded answers a loop group refused, a
	// counter per group (attribute group): LoopStateValue.OutboundRefused.
	MetricLoopOutboundRefused string = "net_loop_outbound_refused_total"
	// MetricLoopBatchP99 names the 99th percentile of a loop group's batch duration, in
	// seconds, a gauge per group (attribute group).
	MetricLoopBatchP99 string = "net_loop_batch_p99_seconds"
)

// The units the instruments carry, in UCUM's annotation form.
const (
	// unitConn counts connections.
	unitConn string = "{connection}"
	// unitBackoff counts waits.
	unitBackoff string = "{backoff}"
	// unitDatagram counts datagrams, sends and replies.
	unitDatagram string = "{datagram}"
	// unitLine counts lines of a log.
	unitLine string = "{line}"
	// unitBatch counts batches.
	unitBatch string = "{batch}"
	// unitCompletion counts completions.
	unitCompletion string = "{completion}"
	// unitRecv counts receives.
	unitRecv string = "{receive}"
	// unitRing counts rings.
	unitRing string = "{ring}"
	// unitAnswer counts answers.
	unitAnswer string = "{answer}"
	// unitSecond is the second.
	unitSecond string = "s"
	// unitState is a dimensionless state, 0 or 1.
	unitState string = "1"
)

// metricsKind says which observable an instrument is registered as.
type metricsKind uint8

const (
	// metricCounter is a monotonic sum, reported as its absolute total.
	metricCounter metricsKind = iota + 1
	// metricGauge is a level, an up-down counter.
	metricGauge
	// metricFloatGauge is a sampled reading in a float.
	metricFloatGauge
)

// metricsSnapshot is what one collection reads: State once, and the
// connections each goroutine-engine stream group counts.
type metricsSnapshot struct {
	// state is the server's State at the start of the collection.
	state corenet.StateValue
	// groups are the stream groups' own connection counts.
	groups []groupConns
}

// metricInstrument is one instrument: its name, unit and kind, and how it
// reads the snapshot. A point is reported with the attributes it adds to the
// caller's.
type metricInstrument struct {
	// name is the instrument's name, a Metric constant.
	name string
	// unit is its UCUM unit.
	unit string
	// kind is what it is registered as.
	kind metricsKind
	// ints reports an integer instrument's points.
	ints func(snap *metricsSnapshot, report func(value int64, attrs ...coreotel.AttrValue))
	// floats reports a float gauge's points.
	floats func(snap *metricsSnapshot, report func(value float64, attrs ...coreotel.AttrValue))
}

// metricInstruments are the instruments RegisterMetrics creates, in the order
// of the Metric constants.
func metricInstruments() []metricInstrument {
	return slices.Concat(serverInstruments(), groupInstruments(), loopInstruments())
}

// groupConns is one stream group's counts at a collection.
type groupConns struct {
	// name is the group's.
	name string
	// active, admitted and rejected are its connCounters values.
	active             int64
	admitted, rejected uint64
}

// groupConnections reads the connection counts of the goroutine-engine stream
// groups, in declaration order. A loop group counts per ring, in State.
func (s *Server) groupConnections() []groupConns {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]groupConns, 0, len(s.groups))
	for _, g := range s.groups {
		//: a loop group's connections are its rings' rows.
		if g.loopHandler != nil {
			continue
		}
		out = append(out, groupConns{
			name: g.name, active: g.conns.active.Load(),
			admitted: g.conns.admitted.Load(), rejected: g.conns.rejected.Load(),
		})
	}
	return out
}

// serverInstruments are the instruments of the server as a whole: no
// attribute beyond the caller's.
func serverInstruments() []metricInstrument {
	scalar := func(read func(corenet.StateValue) int64) func(*metricsSnapshot, func(int64, ...coreotel.AttrValue)) {
		return func(snap *metricsSnapshot, report func(int64, ...coreotel.AttrValue)) { report(read(snap.state)) }
	}
	return []metricInstrument{
		{
			name: MetricAcceptBackoffs, unit: unitBackoff, kind: metricCounter,
			ints: scalar(func(s corenet.StateValue) int64 { return int64(s.AcceptBackoffs) }),
		},
		{
			name: MetricDatagramsTruncated, unit: unitDatagram, kind: metricCounter,
			ints: scalar(func(s corenet.StateValue) int64 { return int64(s.Datagrams.TruncatedPackets) }),
		},
		{
			name: MetricGSOOversized, unit: unitDatagram, kind: metricCounter,
			ints: scalar(func(s corenet.StateValue) int64 { return int64(s.Datagrams.GSOOversized) }),
		},
		{
			name: MetricReplyFailures, unit: unitDatagram, kind: metricCounter,
			ints: scalar(func(s corenet.StateValue) int64 { return int64(s.Datagrams.ReplyFailures) }),
		},
		{
			name: MetricSuppressedLogs, unit: unitLine, kind: metricCounter,
			ints: scalar(func(s corenet.StateValue) int64 { return int64(s.SuppressedLogs) }),
		},
		{
			name: MetricDegraded, unit: unitState, kind: metricGauge,
			ints: scalar(func(s corenet.StateValue) int64 { return boolInt(s.Degraded()) }),
		},
	}
}

// groupInstruments are the stream groups' connections, each series carrying
// the group.
func groupInstruments() []metricInstrument {
	perGroup := func(read func(groupConns) int64) func(*metricsSnapshot, func(int64, ...coreotel.AttrValue)) {
		return func(snap *metricsSnapshot, report func(int64, ...coreotel.AttrValue)) {
			for _, g := range snap.groups {
				report(read(g), coreotel.String("group", g.name))
			}
		}
	}
	return []metricInstrument{
		{name: MetricConnsActive, unit: unitConn, kind: metricGauge, ints: perGroup(func(g groupConns) int64 { return g.active })},
		{name: MetricConnsTotal, unit: unitConn, kind: metricCounter, ints: perGroup(func(g groupConns) int64 { return int64(g.admitted) })},
		{name: MetricConnsRejected, unit: unitConn, kind: metricCounter, ints: perGroup(func(g groupConns) int64 { return int64(g.rejected) })},
	}
}

// loopInstruments are the loop groups': a ring's counters carry the group,
// the loop and the ring; the rest carry the group.
func loopInstruments() []metricInstrument {
	perRing := func(read func(corenet.LoopRingStateValue) uint64) func(*metricsSnapshot, func(int64, ...coreotel.AttrValue)) {
		return func(snap *metricsSnapshot, report func(int64, ...coreotel.AttrValue)) {
			for _, lp := range snap.state.Loops {
				for _, ring := range lp.RingStates {
					report(int64(read(ring)), coreotel.String("group", lp.Group),
						coreotel.Int64("loop", int64(ring.Loop)), coreotel.Int64("ring", int64(ring.Ring)))
				}
			}
		}
	}
	perLoopGroup := func(read func(corenet.LoopStateValue) int64) func(*metricsSnapshot, func(int64, ...coreotel.AttrValue)) {
		return func(snap *metricsSnapshot, report func(int64, ...coreotel.AttrValue)) {
			for _, lp := range snap.state.Loops {
				report(read(lp), coreotel.String("group", lp.Group))
			}
		}
	}
	return []metricInstrument{
		{
			name: MetricLoopBatches, unit: unitBatch, kind: metricCounter,
			ints: perRing(func(r corenet.LoopRingStateValue) uint64 { return r.Batches }),
		},
		{
			name: MetricLoopCompletions, unit: unitCompletion, kind: metricCounter,
			ints: perRing(func(r corenet.LoopRingStateValue) uint64 { return r.Completions }),
		},
		{
			name: MetricLoopBufferStarved, unit: unitRecv, kind: metricCounter,
			ints: perRing(func(r corenet.LoopRingStateValue) uint64 { return r.BufferStarved }),
		},
		{
			name: MetricLoopBatchesCut, unit: unitBatch, kind: metricCounter,
			ints: perRing(func(r corenet.LoopRingStateValue) uint64 { return r.BatchesCut }),
		},
		{
			name: MetricLoopRingsDrained, unit: unitRing, kind: metricGauge,
			ints: perLoopGroup(func(lp corenet.LoopStateValue) int64 { return ringsDraining(lp) }),
		},
		{
			name: MetricLoopOutboundRefused, unit: unitAnswer, kind: metricCounter,
			ints: perLoopGroup(func(lp corenet.LoopStateValue) int64 { return int64(lp.OutboundRefused) }),
		},
		{
			name: MetricLoopBatchP99, unit: unitSecond, kind: metricFloatGauge,
			floats: func(snap *metricsSnapshot, report func(float64, ...coreotel.AttrValue)) {
				for _, lp := range snap.state.Loops {
					report(lp.BatchP99.Duration().Seconds(), coreotel.String("group", lp.Group))
				}
			},
		},
	}
}

// ringsDraining counts the rings of a loop group the watchdog has drained.
func ringsDraining(lp corenet.LoopStateValue) int64 {
	var n int64
	for _, ring := range lp.RingStates {
		n += boolInt(ring.Draining)
	}
	return n
}

// boolInt is 1 for true, 0 for false.
func boolInt(b bool) int64 {
	//: a flag as a gauge's level.
	if b {
		return 1
	}
	return 0
}

// metricsReader reads the server once per collection for every instrument
// registered through it: the meter runs each callback once per collection,
// serialised, so the first callback of a round takes the snapshot and the
// others read it. A meter collected from two goroutines at once is serialised
// here.
type metricsReader struct {
	// s is the server read.
	s *Server
	// attrs are the caller's, riding on every series.
	attrs []coreotel.AttrValue
	// mu guards the round.
	mu sync.Mutex
	// snap is the round's snapshot.
	snap metricsSnapshot
	// served counts the callbacks of the round; callbacks is how many a round
	// runs.
	served, callbacks int
}

// observe runs one instrument's callback: the round's first takes the
// snapshot.
func (r *metricsReader) observe(read func(snap *metricsSnapshot)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: the round's first callback reads the server for all of them.
	if r.served == 0 {
		r.snap = metricsSnapshot{state: r.s.State(), groups: r.s.groupConnections()}
	}
	r.served = (r.served + 1) % r.callbacks
	read(&r.snap)
}

// with is the caller's attributes followed by a point's own.
func (r *metricsReader) with(attrs []coreotel.AttrValue) []coreotel.AttrValue {
	//: the common case, a point with nothing of its own.
	if len(attrs) == 0 {
		return r.attrs
	}
	return append(append(make([]coreotel.AttrValue, 0, len(r.attrs)+len(attrs)), r.attrs...), attrs...)
}

// RegisterMetrics registers the server's counters on m as observables (#847),
// mirroring RegisterHealth: one State read per collection, no cost per
// request. The caller's attrs ride on every series; a stream group's
// connections (MetricConnsActive, MetricConnsTotal, MetricConnsRejected) add
// the group, a loop group's rings (MetricLoopBatches and its siblings) the
// group, the loop and the ring, and the server's own (MetricAcceptBackoffs,
// MetricDegraded and the others) nothing. The names are the Metric
// constants. A nil m, or an m the server is already registered on, is
// refused with MetricsRefused; another meter is another destination.
func (s *Server) RegisterMetrics(m coremetrics.AsyncMeter, attrs ...coreotel.AttrValue) error {
	//: no meter to register on.
	if m == nil {
		return errs.Wrap(corenet.MetricsRefused, errs.WrapParams{}, errs.String("why", "no meter"))
	}
	//: the same meter twice would run every callback twice per collection.
	if !s.claimMeter(m) {
		return errs.Wrap(corenet.MetricsRefused, errs.WrapParams{}, errs.String("why", "already registered"))
	}
	instruments := metricInstruments()
	reader := &metricsReader{s: s, attrs: slices.Clone(attrs), callbacks: len(instruments)}
	units, _ := m.(coremetrics.UnitDescriber)
	//: one observable per instrument, all fed by one reader.
	for _, inst := range instruments {
		registerInstrument(m, reader, inst)
		//: the unit, where the meter keeps one.
		if units != nil {
			units.Unit(inst.name, inst.unit)
		}
	}
	return nil
}

// registerInstrument registers inst on m as the observable its kind names.
func registerInstrument(m coremetrics.AsyncMeter, reader *metricsReader, inst metricInstrument) {
	switch inst.kind {
	//: a float level.
	case metricFloatGauge:
		m.ObservableGauge(inst.name, func(observe coremetrics.ObserveFloat64) {
			reader.observe(func(snap *metricsSnapshot) {
				inst.floats(snap, func(v float64, attrs ...coreotel.AttrValue) { observe(v, reader.with(attrs)...) })
			})
		})
	//: a level of integers.
	case metricGauge:
		m.ObservableUpDownCounter(inst.name, func(observe coremetrics.ObserveInt64) {
			reader.observe(func(snap *metricsSnapshot) {
				inst.ints(snap, func(v int64, attrs ...coreotel.AttrValue) { observe(v, reader.with(attrs)...) })
			})
		})
	//: a count that only grows.
	default:
		m.ObservableCounter(inst.name, func(observe coremetrics.ObserveInt64) {
			reader.observe(func(snap *metricsSnapshot) {
				inst.ints(snap, func(v int64, attrs ...coreotel.AttrValue) { observe(v, reader.with(attrs)...) })
			})
		})
	}
}

// claimMeter records m as a destination, and reports false when the server is
// already registered on it. A meter whose dynamic type is not comparable cannot
// be told apart, and is taken each time.
func (s *Server) claimMeter(m coremetrics.AsyncMeter) bool {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	//: only a comparable meter can be recognised.
	if !reflect.TypeOf(m).Comparable() {
		return true
	}
	//: the same meter, by identity.
	if slices.Contains(s.meters, m) {
		return false
	}
	s.meters = append(s.meters, m)
	return true
}
