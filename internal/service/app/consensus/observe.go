package consensus

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// The metric names are part of the contract (D13): spelled as Prometheus
// exposes them, underscores, _total on counters, units in the name.
const (
	mRole             = "consensus_role"
	mTerm             = "consensus_term"
	mLeaderChanges    = "consensus_leader_changes_total"
	mHasLeader        = "consensus_has_leader"
	mFailureTolerance = "consensus_failure_tolerance"
	mCommitLatency    = "consensus_commit_latency_seconds"
	mApplyLatency     = "consensus_apply_latency_seconds"
	mFsyncLatency     = "consensus_fsync_latency_seconds"
	mBatchEntries     = "consensus_batch_entries"
	mBatchBytes       = "consensus_batch_bytes"
	mApplyLagEntries  = "consensus_apply_lag_entries"
	mApplyLagBytes    = "consensus_apply_lag_bytes"
	mPeerMatchLag     = "consensus_peer_match_lag_entries"
	mPeerInflight     = "consensus_peer_inflight"
	mPeerUnreachable  = "consensus_peer_unreachable"
	mPeerLastContact  = "consensus_peer_last_contact_seconds"
	mElectionsStarted = "consensus_elections_started_total"
	mSnapshotDuration = "consensus_snapshot_duration_seconds"
	mSnapshotBytes    = "consensus_snapshot_bytes"
	mSnapshotInstalls = "consensus_snapshot_installs_total"
	mNoSpace          = "consensus_nospace"
	mFramesDropped    = "consensus_frames_dropped_total"
	mBarrierLatency   = "consensus_barrier_latency_seconds"
	mProposalsFailed  = "consensus_proposals_failed_total"
	mProposalsPending = "consensus_proposals_pending"
	mClusterVersion   = "consensus_cluster_version"
	mProtocolVersion  = "consensus_protocol_version"
	mLoopTurn         = "consensus_loop_turn_seconds"
	mChainRefusals    = "consensus_chain_refusals_total"
	mStopped          = "consensus_stopped"
	mJoinsRateLimited = "consensus_joins_rate_limited_total"
	mLogBytes         = "consensus_log_bytes"
	mLogQuota         = "consensus_log_quota_bytes"
	mFsFree           = "consensus_fs_free_bytes"
	mTailTruncated    = "consensus_tail_truncated_total"
	mCertExpiry       = "consensus_tls_cert_expiry_seconds"
	mPeerCertExpiry   = "consensus_tls_peer_cert_expiry_seconds"
	mReloadFailures   = "consensus_tls_reload_failures_total"
)

// departedCertExpiry is what consensus_tls_peer_cert_expiry_seconds reads
// for a peer that left: a series cannot be deleted, and its last value,
// left to stand, crossed the alert's threshold (#532). It never expires.
const departedCertExpiry float64 = math.MaxFloat64

var (
	// peerGauges are the leader's per-peer series, reset once a peer is no
	// longer published (#532).
	peerGauges = []string{mPeerMatchLag, mPeerInflight, mPeerUnreachable, mPeerLastContact}
	// latencyBuckets are the histogram bounds, in seconds, of every latency
	// D13 records: half a millisecond to five seconds, dense below 100 ms
	// where a healthy commit lands, so a regression is visible before it is
	// an outage.
	latencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
)

// obs holds a node's instruments. Every method is safe on a nil Meter,
// Tracer or Logger: observability is optional and never changes a decision.
type obs struct {
	meter  coremetrics.Meter
	tracer coretrace.Tracer
	log    corelogger.Logger
	group  coreotel.AttrValue

	mu    sync.Mutex
	gauge map[string]coremetrics.Gauge
	count map[string]coremetrics.Counter
	hist  map[string]coremetrics.Histogram

	lastPeers time.Time
	lastRes   time.Time
	// peersPub and certsPub are the peers whose series the last pass
	// published: a series is never deleted, so a peer that left is reset
	// rather than left at its last value (#532).
	peersPub, certsPub map[coreconsensus.NodeID]bool
	// seenTails and seenReloads are the running totals already added to
	// their counters.
	seenTails, seenReloads int64
}

// newObs binds a node's instruments to its configured Meter, Tracer and
// Logger; every series carries the group, so two groups never share one.
func newObs(cfg *Config) *obs {
	return &obs{
		meter: cfg.Meter, tracer: cfg.Tracer, log: cfg.Logger,
		group: coreotel.Int64("group", int64(cfg.Group)),
		gauge: map[string]coremetrics.Gauge{}, count: map[string]coremetrics.Counter{}, hist: map[string]coremetrics.Histogram{},
	}
}

// key is the cache key of one series: the name and its attributes in call
// order. A bare name is its own key, so the unlabelled series, the common
// case, costs no allocation.
func key(name string, attrs []coreotel.AttrValue) string {
	if len(attrs) == 0 {
		return name
	}
	var k strings.Builder
	k.WriteString(name)
	for _, a := range attrs {
		k.WriteString("|")
		k.WriteString(a.Key)
		k.WriteString("=")
		k.WriteString(a.Str())
	}
	return k.String()
}

// setGauge sets a gauge series, creating it on first use; a nil Meter makes
// it a no-op.
func (o *obs) setGauge(name string, v float64, attrs ...coreotel.AttrValue) {
	if o.meter == nil {
		return
	}
	k := key(name, attrs)
	o.mu.Lock()
	g, ok := o.gauge[k]
	if !ok {
		g = o.meter.Gauge(name, append([]coreotel.AttrValue{o.group}, attrs...)...)
		o.gauge[k] = g
	}
	o.mu.Unlock()
	g.Set(v)
}

// inc adds one to a counter series, creating it on first use; a nil Meter
// makes it a no-op.
func (o *obs) inc(name string, attrs ...coreotel.AttrValue) {
	if o.meter == nil {
		return
	}
	k := key(name, attrs)
	o.mu.Lock()
	c, ok := o.count[k]
	if !ok {
		c = o.meter.Counter(name, append([]coreotel.AttrValue{o.group}, attrs...)...)
		o.count[k] = c
	}
	o.mu.Unlock()
	c.Inc()
}

// addTo brings a counter up to a running total kept elsewhere.
func (o *obs) addTo(name string, seen *int64, total int64) {
	if o.meter == nil || total <= *seen {
		return
	}
	k := key(name, nil)
	o.mu.Lock()
	c, ok := o.count[k]
	if !ok {
		c = o.meter.Counter(name, o.group)
		o.count[k] = c
	}
	o.mu.Unlock()
	c.Add(total - *seen)
	*seen = total
}

// record adds one observation to a histogram series, creating it on first
// use with byte-scale buckets for the size metrics and latency buckets for
// the rest; a nil Meter makes it a no-op.
func (o *obs) record(name string, v float64, attrs ...coreotel.AttrValue) {
	if o.meter == nil {
		return
	}
	k := key(name, attrs)
	o.mu.Lock()
	h, ok := o.hist[k]
	if !ok {
		buckets := latencyBuckets
		if name == mBatchEntries || name == mBatchBytes || name == mSnapshotBytes {
			buckets = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 1 << 20, 16 << 20, 256 << 20}
		}
		h = o.meter.Histogram(name, buckets, append([]coreotel.AttrValue{o.group}, attrs...)...)
		o.hist[k] = h
	}
	o.mu.Unlock()
	h.Record(v)
}

// info logs at Info, without building the record when the level is off.
func (o *obs) info(msg string, attrs ...corelogger.AttrValue) {
	if o.log != nil && o.log.Enabled(context.Background(), level.Info) {
		o.log.Log(context.Background(), level.Info, msg, attrs...)
	}
}

// warn logs at Warn, without building the record when the level is off.
func (o *obs) warn(msg string, attrs ...corelogger.AttrValue) {
	if o.log != nil && o.log.Enabled(context.Background(), level.Warn) {
		o.log.Log(context.Background(), level.Warn, msg, attrs...)
	}
}

// lattr is an unsigned log attribute.
func lattr(k string, v uint64) corelogger.AttrValue {
	return corelogger.AttrValue{Key: k, Value: corelogger.Uint64Value(v)}
}

// lstr is a string log attribute.
func lstr(k, v string) corelogger.AttrValue {
	return corelogger.AttrValue{Key: k, Value: corelogger.StringValue(v)}
}

// span starts a span when a Tracer is configured; the returned span may be
// nil, and every helper below accepts it.
func (o *obs) span(ctx context.Context, name string) coretrace.Span {
	if o.tracer == nil {
		return nil
	}
	_, s := o.tracer.Start(ctx, name, coretrace.SpanParams{Attrs: []coreotel.AttrValue{o.group}})
	return s
}

// spanEvent records an event on a span that may be nil (no Tracer).
func spanEvent(s coretrace.Span, name string, attrs ...coreotel.AttrValue) {
	if s != nil {
		s.AddEvent(name, attrs...)
	}
}

// spanEnd ends a span that may be nil, marking it failed when err is set.
func spanEnd(s coretrace.Span, err error) {
	if s == nil {
		return
	}
	if err != nil {
		s.SetStatus(coretrace.StatusError, err.Error())
	}
	s.End()
}

// codeAttr is the attribute a failure is counted under: the error's reason,
// or its dotted-quad code when it has none, so the series stays bounded by
// the code space rather than by the messages.
func codeAttr(err error) coreotel.AttrValue {
	c, _ := errs.CodeOf(err)
	r, _ := errs.ReasonOf(err)
	if r == "" {
		r = c.String()
	}
	return coreotel.String("code", r)
}

// observeStatus publishes the gauges a status turn changes, and logs role
// and term changes (D13).
func (n *Node) observeStatus(old, cur *coreconsensus.StatusValue) {
	o := n.obs
	o.setGauge(mRole, float64(cur.Role))
	o.setGauge(mTerm, float64(cur.Term))
	hl := 0.0
	if cur.Leader != 0 {
		hl = 1
	}
	o.setGauge(mHasLeader, hl)
	o.setGauge(mApplyLagEntries, float64(cur.Commit-min(cur.Commit, cur.Applied)))
	ns := 0.0
	if cur.NoSpace != 0 {
		ns = 1
	}
	o.setGauge(mNoSpace, ns)
	o.setGauge(mClusterVersion, float64(cur.Versions.Cluster))
	o.setGauge(mProtocolVersion, float64(cur.Versions.ProtocolMax))
	if old == nil || old.Leader != cur.Leader || old.Term != cur.Term || old.Role != cur.Role {
		o.observeRoleChange(old, cur)
	}
}

// observeRoleChange counts a leader change and an election start, and logs
// the new role and term; it runs only on a turn where one of them moved, so
// a stable leader logs nothing.
func (o *obs) observeRoleChange(old, cur *coreconsensus.StatusValue) {
	if old != nil && old.Leader != cur.Leader {
		o.inc(mLeaderChanges)
	}
	if cur.Role == coreconsensus.RolePreCandidate || cur.Role == coreconsensus.RoleCandidate {
		o.inc(mElectionsStarted)
	}
	o.info("consensus: role or term changed", lstr("role", cur.Role.String()), lattr("term", cur.Term),
		lattr("leader", uint64(cur.Leader)))
}

// observePeers publishes the leader's per-peer gauges and the failure
// tolerance, at most every 100 ms, on the loop.
func (n *Node) observePeers() {
	o := n.obs
	if o.meter == nil {
		return
	}
	now := n.clk.Now()
	if now.Sub(o.lastPeers) < 100*time.Millisecond {
		return
	}
	o.lastPeers = now
	o.setGauge(mFailureTolerance, float64(n.r.FailureTolerance(now)))
	last := n.r.Status().Last
	window := n.r.Status().Timing.ElectionTimeoutMin
	seen := map[coreconsensus.NodeID]bool{}
	for _, p := range n.r.PeerStats() {
		seen[p.ID] = true
		n.observePeer(&p, now, last, window)
	}
	o.resetDeparted(seen)
}

// observePeer publishes one peer's gauges.
func (n *Node) observePeer(p *raft.PeerStatValue, now time.Time, last uint64, window time.Duration) {
	peer := coreotel.String("peer", p.ID.String())
	n.obs.setGauge(mPeerMatchLag, float64(last-min(last, p.Match)), peer)
	n.obs.setGauge(mPeerInflight, float64(p.Inflight), peer)
	u := 0.0
	if !p.Flags.Has(raft.StatHeard) || now.Sub(p.LastHeard) >= window {
		u = 1
	}
	if at, ok := n.unreachableAt[p.ID]; ok && at.After(p.LastHeard) {
		u = 1
	}
	n.obs.setGauge(mPeerUnreachable, u, peer)
	if p.Flags.Has(raft.StatHeard) {
		n.obs.setGauge(mPeerLastContact, now.Sub(p.LastHeard).Seconds(), peer)
	}
}

// resetDeparted resets once the gauges of every peer the last pass
// published and this one, whose peers are seen, did not: a peer that left
// the membership, or a node no longer leading, publishes nothing more of
// it, its last value stood for good, and "peer unreachable" alerted for a
// member that no longer existed (#532). Its series reads as a quiet peer.
func (o *obs) resetDeparted(seen map[coreconsensus.NodeID]bool) {
	for id := range o.peersPub {
		if seen[id] {
			continue
		}
		peer := coreotel.String("peer", id.String())
		for _, name := range peerGauges {
			o.setGauge(name, 0, peer)
		}
	}
	o.peersPub = seen
}

// observeResources publishes the log, filesystem and certificate gauges and
// the counters the stores and the transport keep, at most every second, on
// the loop (D13).
func (n *Node) observeResources() {
	o := n.obs
	if o.meter == nil {
		return
	}
	now := n.clk.Now()
	if now.Sub(o.lastRes) < time.Second {
		return
	}
	o.lastRes = now
	o.setGauge(mLogQuota, float64(n.cfg.MaxLogBytes))
	if ls, ok := n.cfg.Log.(interface {
		logStats() (bytes, free, tails int64)
	}); ok {
		b, free, tails := ls.logStats()
		o.setGauge(mLogBytes, float64(b))
		if free >= 0 {
			o.setGauge(mFsFree, float64(free), coreotel.String("fs", "data"))
		}
		o.addTo(mTailTruncated, &o.seenTails, tails)
	}
	if ct, ok := n.cfg.Transport.(interface {
		CertExpiry() time.Time
		PeerCertExpiry() map[coreconsensus.NodeID]time.Time
		ReloadFailures() int
	}); ok {
		if e := ct.CertExpiry(); !e.IsZero() {
			o.setGauge(mCertExpiry, e.Sub(now).Seconds())
		}
		seen := map[coreconsensus.NodeID]bool{}
		for id, e := range ct.PeerCertExpiry() {
			seen[id] = true
			o.setGauge(mPeerCertExpiry, e.Sub(now).Seconds(), coreotel.String("peer", id.String()))
		}
		for id := range o.certsPub {
			if !seen[id] {
				o.setGauge(mPeerCertExpiry, departedCertExpiry, coreotel.String("peer", id.String()))
			}
		}
		o.certsPub = seen
		o.addTo(mReloadFailures, &o.seenReloads, int64(ct.ReloadFailures()))
	}
}
