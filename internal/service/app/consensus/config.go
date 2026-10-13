package consensus

import (
	"math"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// batchFrameOverhead bounds what an append frame adds to the batch bytes
// the leader counts: the message header and the first entry's fields.
const batchFrameOverhead int = 1 << 10

// ReadMode selects how Barrier confirms leadership (D9).
type ReadMode uint8

const (
	// ReadIndex confirms every read by a heartbeat round started after it
	// arrived. It is the zero value and the default.
	ReadIndex ReadMode = iota
	// ReadLease serves reads under the leader lease, sound only under the
	// four conditions of D9; it requires InitialTiming.MaxClockDrift.
	ReadLease
)

// Durability says where the state machine's state lives (D3).
type Durability uint8

const (
	// DurabilityUnset is refused: the caller states it.
	DurabilityUnset Durability = iota
	// DurabilityVolatile is a state machine held in memory, rebuilt from the
	// latest snapshot and the log at every start.
	DurabilityVolatile
	// DurabilityDurable is a DurableStateMachine that persists its applied
	// position with its state.
	DurabilityDurable
)

// Config builds a Node. Every bound has a default and a validation (D14).
type Config struct {
	// Cluster is the cluster the node belongs to; it must match the stamp.
	Cluster coreconsensus.ClusterID
	// Group is the consensus group, 1 in v1; 0 is clamped to 1.
	Group coreconsensus.GroupID
	// ID is the node; 0 takes the stamp's, any other value must match it.
	ID coreconsensus.NodeID
	// Machine is the replicated program.
	Machine coreconsensus.StateMachine
	// Durability must agree with Machine's siblings.
	Durability Durability
	// Log, Snapshots and Transport are the node's ports; the caller owns
	// them and closes them after the node.
	Log       coreconsensus.LogStore
	Snapshots coreconsensus.SnapshotStore
	Transport coreconsensus.Transport
	// Clock drives every timer; nil is clock.System.
	Clock clock.Timed
	// InitialTiming is proposed at Bootstrap only; the committed contract
	// rules.
	InitialTiming coreconsensus.TimingContractSpec

	MaxBatchBytes, MaxInflight, MaxInflightBytes, MaxEntryBytes int
	MaxPendingBytes, MaxApplyLagBytes, RecentEntriesBytes       int64
	SnapshotEvery, SnapshotTrailing, PromoteLag                 uint64
	SnapshotEveryBytes, MaxSnapshotBytes, MaxLogBytes           int64
	LogReserveBytes                                             int64

	// SnapshotCodec compresses snapshot chunks; nil is flate.
	SnapshotCodec coretransform.BoundedDecompressor
	// SnapshotPausesApply says the state machine cannot cut cheaply.
	SnapshotPausesApply bool
	// MinVoters is the floor a Remove may not cross.
	MinVoters int
	// JoinAuthorizer decides a JOIN; without it every JOIN is refused.
	JoinAuthorizer coreconsensus.JoinAuthorizer
	// HardCloseTimeout bounds HardClose.
	HardCloseTimeout time.Duration
	// Reads selects the read mode.
	Reads ReadMode
	// PreferredLeaders are the nodes a leader transfers to after an
	// election when it is not one of them.
	PreferredLeaders []coreconsensus.NodeID
	// Seed draws the election timeouts; 0 draws one.
	Seed uint64
	// Meter, Tracer and Logger receive D13's metrics, one span per Propose
	// and the role, membership, snapshot and truncation events; nil turns
	// each off. None of them changes a decision.
	Meter  coremetrics.Meter
	Tracer coretrace.Tracer
	Logger corelogger.Logger
}

// TimingDefaults are D5's defaults for the RTT matrix of the three sites.
func TimingDefaults() coreconsensus.TimingContractSpec {
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second,
	}
}

// misconfigured is the refusal of a Config the node cannot run on: it names
// the field and the reason, never the value the caller supplied.
func misconfigured(field, why string) error {
	return errs.Wrap(coreconsensus.ConsensusMisconfigured, errs.WrapParams{},
		errs.String("field", field), errs.String("why", why))
}

// clampInt replaces a non-positive bound with its default: a zero bound is
// "unset", never "unlimited" (ADR 0031, clamp half).
func clampInt(v *int, d int) {
	if *v <= 0 {
		*v = d
	}
}

// clampInt64 is clampInt for the byte budgets held as int64.
func clampInt64(v *int64, d int64) {
	if *v <= 0 {
		*v = d
	}
}

// clampU64 is clampInt for the unsigned counts, where only zero is unset.
func clampU64(v *uint64, d uint64) {
	if *v == 0 {
		*v = d
	}
}

// withDefaults applies the clamp half of ADR 0031, then the cross-field
// refusals (R2-14), to a copy of cfg: the caller's Config is never changed.
func withDefaults(cfg *Config) (Config, error) {
	c := *cfg
	clampDefaults(&c)
	if err := refuseIncoherent(&c); err != nil {
		return c, err
	}
	return c, refuseDurability(&c)
}

// clampDefaults fills every unset field with its default; it refuses
// nothing, so the refusals that follow judge the configuration the node
// would actually run on.
func clampDefaults(c *Config) {
	if c.Group == 0 {
		c.Group = 1
	}
	if c.Clock == nil {
		c.Clock = clock.System
	}
	clampTiming(&c.InitialTiming)
	clampInt(&c.MaxBatchBytes, 1<<20)
	clampInt(&c.MaxInflight, 64)
	clampInt(&c.MaxInflightBytes, 8<<20)
	clampInt(&c.MaxEntryBytes, 1<<20)
	clampInt64(&c.MaxPendingBytes, 64<<20)
	clampInt64(&c.MaxApplyLagBytes, 256<<20)
	clampInt64(&c.RecentEntriesBytes, 32<<20)
	clampU64(&c.SnapshotEvery, 16384)
	clampInt64(&c.SnapshotEveryBytes, 64<<20)
	clampU64(&c.SnapshotTrailing, 4096)
	clampInt64(&c.LogReserveBytes, 256<<20)
	clampInt(&c.MinVoters, 3)
	clampU64(&c.PromoteLag, 1024)
	if c.HardCloseTimeout <= 0 {
		c.HardCloseTimeout = 5 * time.Second
	}
	if c.SnapshotCodec == nil {
		c.SnapshotCodec = defaultCodec()
	}
}

// clampTiming fills the unset fields of the proposed timing contract from
// D5's defaults; an unset maximum is at least twice the minimum, so a
// caller who raised only the minimum still gets a valid window.
func clampTiming(timing *coreconsensus.TimingContractSpec) {
	def := TimingDefaults()
	if timing.Generation == 0 {
		timing.Generation = 1
	}
	if timing.HeartbeatInterval <= 0 {
		timing.HeartbeatInterval = def.HeartbeatInterval
	}
	if timing.ElectionTimeoutMin <= 0 {
		timing.ElectionTimeoutMin = def.ElectionTimeoutMin
	}
	if timing.ElectionTimeoutMax <= 0 {
		timing.ElectionTimeoutMax = max(def.ElectionTimeoutMax, 2*timing.ElectionTimeoutMin)
	}
}

// refuseIncoherent returns the first cross-field refusal (R2-14), always in
// the same order (the ports, then the timing, then the byte bounds), so a
// caller fixing one at a time sees a stable sequence.
func refuseIncoherent(c *Config) error {
	if err := refuseNilPorts(c); err != nil {
		return err
	}
	if err := refuseTiming(c); err != nil {
		return err
	}
	return refuseBounds(c)
}

// refuseNilPorts refuses a node missing one of the four ports it cannot
// run without.
func refuseNilPorts(c *Config) error {
	switch {
	case c.Machine == nil:
		return misconfigured("Machine", "nil")
	case c.Log == nil:
		return misconfigured("Log", "nil")
	case c.Snapshots == nil:
		return misconfigured("Snapshots", "nil")
	case c.Transport == nil:
		return misconfigured("Transport", "nil")
	}
	return nil
}

// refuseTiming refuses a timing contract under which elections would fire
// between heartbeats, and a lease read without a bounded clock drift (D9).
func refuseTiming(c *Config) error {
	timing := &c.InitialTiming
	switch {
	case timing.HeartbeatInterval*5 > timing.ElectionTimeoutMin:
		return misconfigured("InitialTiming.HeartbeatInterval", "not below a fifth of ElectionTimeoutMin")
	case timing.ElectionTimeoutMax <= timing.ElectionTimeoutMin:
		return misconfigured("InitialTiming.ElectionTimeoutMax", "not above ElectionTimeoutMin")
	case c.Reads == ReadLease && (math.IsNaN(timing.MaxClockDrift) || timing.MaxClockDrift <= 0 || timing.MaxClockDrift >= 1):
		return misconfigured("InitialTiming.MaxClockDrift", "ReadLease needs 0 < drift < 1")
	}
	return nil
}

// refuseBounds refuses byte bounds that contradict each other, a batch the
// transport cannot carry, and the two ceilings that have no safe default
// (D14).
func refuseBounds(c *Config) error {
	switch {
	case c.MaxInflightBytes < c.MaxBatchBytes:
		return misconfigured("MaxInflightBytes", "below MaxBatchBytes")
	case c.MaxEntryBytes > c.MaxBatchBytes:
		return misconfigured("MaxEntryBytes", "above MaxBatchBytes")
	case framesBelow(c.Transport, c.MaxBatchBytes+batchFrameOverhead):
		// A batch the transport cannot carry stalls every follower it is
		// sent to (review M2).
		return misconfigured("MaxBatchBytes", "above the transport's MaxFrameBytes")
	case c.MaxSnapshotBytes <= 0:
		return misconfigured("MaxSnapshotBytes", "zero")
	case c.MaxSnapshotBytes < c.SnapshotEveryBytes:
		return misconfigured("MaxSnapshotBytes", "below SnapshotEveryBytes")
	case c.MaxLogBytes <= 0:
		return misconfigured("MaxLogBytes", "zero")
	case c.LogReserveBytes >= c.MaxLogBytes:
		return misconfigured("LogReserveBytes", "not below MaxLogBytes")
	}
	return nil
}

// refuseDurability refuses a Durability that disagrees with the machine's
// siblings, and the unset zero (D3): guessing it would either lose state at
// restart or replay entries a durable machine already applied.
func refuseDurability(c *Config) error {
	_, durable := c.Machine.(coreconsensus.DurableStateMachine)
	switch c.Durability {
	case DurabilityVolatile:
		if durable {
			return misconfigured("Durability", "volatile, but the machine is a DurableStateMachine")
		}
	case DurabilityDurable:
		if !durable {
			return misconfigured("Durability", "durable, but the machine has no AppliedPosition")
		}
	default:
		return misconfigured("Durability", "unset")
	}
	return nil
}

// MemoryBudget is the node's memory budget with two peers, the sum of its
// bounds (D14).
func (c Config) MemoryBudget(peers int) int64 {
	return c.MaxPendingBytes + c.MaxApplyLagBytes + c.RecentEntriesBytes + int64(peers)*int64(c.MaxInflightBytes)
}

// defaultCodec is flate, whose registered singleton is known to bound its
// decompression, so the assertion cannot fail.
func defaultCodec() coretransform.BoundedDecompressor {
	return svctransform.FlateCompressor.(coretransform.BoundedDecompressor)
}

// framesBelow reports whether the transport declares a frame limit below n.
// A transport that declares none is not checked.
func framesBelow(t coreconsensus.Transport, n int) bool {
	f, ok := t.(interface{ maxFrameBytes() int })
	return ok && f.maxFrameBytes() < n
}
