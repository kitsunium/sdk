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
	InitialTiming coreconsensus.TimingContract

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
func TimingDefaults() coreconsensus.TimingContract {
	return coreconsensus.TimingContract{
		Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second,
	}
}

func misconfigured(field, why string) error {
	return errs.Wrap(coreconsensus.ConsensusMisconfigured, errs.WrapParams{},
		errs.String("field", field), errs.String("why", why))
}

func clampInt(v *int, d int) {
	if *v <= 0 {
		*v = d
	}
}

func clampInt64(v *int64, d int64) {
	if *v <= 0 {
		*v = d
	}
}

func clampU64(v *uint64, d uint64) {
	if *v == 0 {
		*v = d
	}
}

// withDefaults applies the clamp half of ADR 0031, then the cross-field
// refusals (R2-14).
func (c Config) withDefaults() (Config, error) {
	if c.Group == 0 {
		c.Group = 1
	}
	if c.Clock == nil {
		c.Clock = clock.System
	}
	t := &c.InitialTiming
	def := TimingDefaults()
	if t.Generation == 0 {
		t.Generation = 1
	}
	if t.HeartbeatInterval <= 0 {
		t.HeartbeatInterval = def.HeartbeatInterval
	}
	if t.ElectionTimeoutMin <= 0 {
		t.ElectionTimeoutMin = def.ElectionTimeoutMin
	}
	if t.ElectionTimeoutMax <= 0 {
		t.ElectionTimeoutMax = max(def.ElectionTimeoutMax, 2*t.ElectionTimeoutMin)
	}
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
	switch {
	case c.Machine == nil:
		return c, misconfigured("Machine", "nil")
	case c.Log == nil:
		return c, misconfigured("Log", "nil")
	case c.Snapshots == nil:
		return c, misconfigured("Snapshots", "nil")
	case c.Transport == nil:
		return c, misconfigured("Transport", "nil")
	case t.HeartbeatInterval*5 > t.ElectionTimeoutMin:
		return c, misconfigured("InitialTiming.HeartbeatInterval", "not below a fifth of ElectionTimeoutMin")
	case t.ElectionTimeoutMax <= t.ElectionTimeoutMin:
		return c, misconfigured("InitialTiming.ElectionTimeoutMax", "not above ElectionTimeoutMin")
	case c.Reads == ReadLease && (math.IsNaN(t.MaxClockDrift) || t.MaxClockDrift <= 0 || t.MaxClockDrift >= 1):
		return c, misconfigured("InitialTiming.MaxClockDrift", "ReadLease needs 0 < drift < 1")
	case c.MaxInflightBytes < c.MaxBatchBytes:
		return c, misconfigured("MaxInflightBytes", "below MaxBatchBytes")
	case c.MaxEntryBytes > c.MaxBatchBytes:
		return c, misconfigured("MaxEntryBytes", "above MaxBatchBytes")
	case c.MaxSnapshotBytes <= 0:
		return c, misconfigured("MaxSnapshotBytes", "zero")
	case c.MaxSnapshotBytes < c.SnapshotEveryBytes:
		return c, misconfigured("MaxSnapshotBytes", "below SnapshotEveryBytes")
	case c.MaxLogBytes <= 0:
		return c, misconfigured("MaxLogBytes", "zero")
	case c.LogReserveBytes >= c.MaxLogBytes:
		return c, misconfigured("LogReserveBytes", "not below MaxLogBytes")
	}
	_, durable := c.Machine.(coreconsensus.DurableStateMachine)
	switch c.Durability {
	case DurabilityVolatile:
		if durable {
			return c, misconfigured("Durability", "volatile, but the machine is a DurableStateMachine")
		}
	case DurabilityDurable:
		if !durable {
			return c, misconfigured("Durability", "durable, but the machine has no AppliedPosition")
		}
	default:
		return c, misconfigured("Durability", "unset")
	}
	return c, nil
}

// MemoryBudget is the node's memory budget with two peers, the sum of its
// bounds (D14).
func (c Config) MemoryBudget(peers int) int64 {
	return c.MaxPendingBytes + c.MaxApplyLagBytes + c.RecentEntriesBytes + int64(peers)*int64(c.MaxInflightBytes)
}

func defaultCodec() coretransform.BoundedDecompressor {
	return svctransform.FlateCompressor.(coretransform.BoundedDecompressor)
}
