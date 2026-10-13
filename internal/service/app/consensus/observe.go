package consensus

import (
	"context"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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

var latencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

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
	// seenTails and seenReloads are the running totals already added to
	// their counters.
	seenTails, seenReloads int64
}

func newObs(cfg Config) *obs {
	return &obs{meter: cfg.Meter, tracer: cfg.Tracer, log: cfg.Logger,
		group: coreotel.Int64("group", int64(cfg.Group)),
		gauge: map[string]coremetrics.Gauge{}, count: map[string]coremetrics.Counter{}, hist: map[string]coremetrics.Histogram{}}
}

func key(name string, attrs []coreotel.AttrValue) string {
	k := name
	for _, a := range attrs {
		k += "|" + a.Key + "=" + a.Str()
	}
	return k
}

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

func (o *obs) info(msg string, attrs ...corelogger.AttrValue) {
	if o.log != nil && o.log.Enabled(context.Background(), level.Info) {
		o.log.Log(context.Background(), level.Info, msg, attrs...)
	}
}

func (o *obs) warn(msg string, attrs ...corelogger.AttrValue) {
	if o.log != nil && o.log.Enabled(context.Background(), level.Warn) {
		o.log.Log(context.Background(), level.Warn, msg, attrs...)
	}
}

func lattr(k string, v uint64) corelogger.AttrValue {
	return corelogger.AttrValue{Key: k, Value: corelogger.Uint64Value(v)}
}

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

func spanEvent(s coretrace.Span, name string, attrs ...coreotel.AttrValue) {
	if s != nil {
		s.AddEvent(name, attrs...)
	}
}

func spanEnd(s coretrace.Span, err error) {
	if s == nil {
		return
	}
	if err != nil {
		s.SetStatus(coretrace.StatusError, err.Error())
	}
	s.End()
}

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
func (n *Node) observeStatus(old, cur *coreconsensus.Status) {
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
		if old != nil && old.Leader != cur.Leader {
			o.inc(mLeaderChanges)
		}
		if cur.Role == coreconsensus.RolePreCandidate || cur.Role == coreconsensus.RoleCandidate {
			o.inc(mElectionsStarted)
		}
		o.info("consensus: role or term changed", lstr("role", cur.Role.String()), lattr("term", cur.Term),
			lattr("leader", uint64(cur.Leader)))
	}
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
	for _, p := range n.r.PeerStats() {
		peer := coreotel.String("peer", p.ID.String())
		o.setGauge(mPeerMatchLag, float64(last-min(last, p.Match)), peer)
		o.setGauge(mPeerInflight, float64(p.Inflight), peer)
		u := 0.0
		if !p.Heard || now.Sub(p.LastHeard) >= window {
			u = 1
		}
		if at, ok := n.unreachableAt[p.ID]; ok && at.After(p.LastHeard) {
			u = 1
		}
		o.setGauge(mPeerUnreachable, u, peer)
		if p.Heard {
			o.setGauge(mPeerLastContact, now.Sub(p.LastHeard).Seconds(), peer)
		}
	}
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
	if ls, ok := n.cfg.Log.(interface{ logStats() (int64, int64, int64) }); ok {
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
		for id, e := range ct.PeerCertExpiry() {
			o.setGauge(mPeerCertExpiry, e.Sub(now).Seconds(), coreotel.String("peer", id.String()))
		}
		o.addTo(mReloadFailures, &o.seenReloads, int64(ct.ReloadFailures()))
	}
}
