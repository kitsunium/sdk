package stream

import (
	"context"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	"github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// The two ways [Subscribe] moves a group's offset past a batch its handler
// accepted. The zero is the subscriber's own commit, which asks nothing of
// the handler.
const (
	// CommitAfterHandler is the zero CommitMode: once the handler returns nil,
	// the subscriber commits the offset after the batch's last record on the
	// stream's [corestream.Committer]. A stop or a crash between that return
	// and the commit delivers the batch again — at least once, on every engine.
	CommitAfterHandler CommitMode = 0
	// CommitByHandler leaves the commit to the handler, which calls the
	// stream's [corestream.Committer] Commit itself — the group, the batch's
	// partition, the offset after its last record — inside the transaction that
	// holds its effect. On the SQL engine the effect and the offset then commit
	// or roll back together: exactly-once in effect. On an engine whose cursors
	// live apart from the handler's effect — memory, file — it stays at least
	// once, the cursor durable after the handler. The subscriber commits
	// nothing for a batch the handler accepted, only past a record it parked.
	CommitByHandler CommitMode = 1
)

// The values a zero field of SubscriberConfig is read as (ADR 0031: each zero
// has one reading, "no opinion", and each value is defensible for any stream).
const (
	// subscriberPollInterval is the queue consumer's default poll, for the
	// queue consumer's reason: a tenth of a second of idle latency for what
	// another process appended, the Waker covering this process at once.
	subscriberPollInterval time.Duration = 100 * time.Millisecond
	// subscriberSettleTimeout is the queue consumer's default settle bound.
	subscriberSettleTimeout time.Duration = 5 * time.Second
	// retryBase, retryCeiling and retryJitter are the curve a zero Retry
	// reads as: 100 ms doubling to 30 s, widened by up to a fifth.
	retryBase    time.Duration = 100 * time.Millisecond
	retryCeiling time.Duration = 30 * time.Second
	retryJitter  float64       = 0.2
	// retryMultiplier is the curve's growth factor.
	retryMultiplier float64 = 2
)

// CommitMode is who moves a group's offset past a batch the handler
// accepted: [CommitAfterHandler] (the zero) or [CommitByHandler].
type CommitMode uint8

// Handler processes one batch of a partition's records, in offset order:
// entries is never empty and holds one partition only. A nil return
// accepts the whole batch; an error, or a panic, refuses it, and the batch
// is handed back after the retry wait. The entries' bytes are the
// handler's for the call; a handler that keeps them past it copies them.
type Handler func(ctx context.Context, entries []corestream.EntryValue) error

// ParkConfig sends the records a handler keeps refusing to a queue as dead
// letters, so their partition moves on: the dead letter's payload is the
// record's [corestream.EntryValue] encoded by encoding/json, which
// json.Unmarshal decodes back into one.
type ParkConfig struct {
	// After is how many consecutive refusals park a record: a batch
	// refused After times is narrowed to its first record, and a record
	// refused After times alone is parked. Below 1 it is read as 1.
	After int
	// Broker is the queue the dead letters are published to. It must be
	// non-nil; its MaxMessageBytes must hold the JSON of the largest
	// record, or the park is refused and Subscribe returns that refusal.
	Broker corequeue.Broker
}

// SubscriberConfig configures [Subscribe]: the group, its handler and the
// one promise about it the SDK cannot check, which partitions, how the
// offset moves, and what a failure, a stop and a second process do.
// Group, Handler and HandlerIsIdempotent are required; every other field's
// zero works.
type SubscriberConfig struct {
	// BatchSize is the most records one Read hands the handler. Below 1
	// it is read as 1.
	BatchSize int
	// Clock times the idle wait, the retry wait, the bound on what is sent
	// past a stop and the fence's renewals. Nil means clock.System;
	// nothing in the subscriber calls time.Sleep.
	Clock clock.Timed
	// Commit is who moves the offset past an accepted batch:
	// [CommitAfterHandler], the zero, or [CommitByHandler]. Any other
	// value is refused.
	Commit CommitMode
	// Fence, when set, makes each worker hold the lock
	// "stream:<Name>:<Group>:<partition>" while it reads its partition,
	// so the same group in two processes never reads one partition
	// twice at once. Nil fences nothing: one process runs the group.
	Fence corelock.Locker
	// Group names the consumer group whose offsets are read and
	// committed. It must not be empty; two groups over one stream read
	// every record each, at their own pace.
	Group string
	// Handler processes each batch. It must be non-nil.
	Handler Handler
	// HandlerIsIdempotent asserts, in code, that Handler is safe to run
	// more than once for the same records. It must be true; its zero is
	// refused, as queue.ConsumerConfig's is: under [CommitAfterHandler]
	// a batch is delivered at least once, and under [CommitByHandler]
	// the assertion is that the handler's effect and its commit are one
	// transaction, which the SDK cannot see either.
	HandlerIsIdempotent bool
	// Name names the stream in the fence's lock names. It is required
	// when Fence is set, and read for nothing else.
	Name string
	// Park, when set, sends a record its handler keeps refusing to a
	// queue and moves past it. Nil retries forever: the partition stays
	// blocked until the handler accepts.
	Park *ParkConfig
	// Partitions are the partitions this subscriber reads, one worker
	// each: static assignment, two processes splitting a group naming
	// disjoint sets (or both every partition, under Fence). Empty means
	// every partition of the stream; a partition outside it, or named
	// twice, is refused.
	Partitions []int
	// PollInterval bounds how long an idle worker waits before reading
	// again: the stream's Waker ends the wait at once on an Append in this
	// process, so it bounds only what another process appended. Zero, or
	// negative, means 100 ms.
	PollInterval time.Duration
	// Retry is the wait between two deliveries of a refused batch, its
	// attempt counting the consecutive refusals. A zero BaseDelay means
	// 100 ms doubling to 30 s with a fifth of jitter: a curve with no wait
	// would spin on a failing handler.
	Retry backoff.Value
	// SettleTimeout bounds each commit, dead letter and fence release
	// sent once ctx has ended, on Clock. Zero, or negative, means 5 s.
	SettleTimeout time.Duration
}

// subscriberMisconfigured refuses the configuration field names.
func subscriberMisconfigured(field string) error {
	//: SubscriberMisconfigured, naming the field.
	return errs.Wrap(corestream.SubscriberMisconfigured, errs.WrapParams{}, errs.String("field", field))
}

// validate refuses a subscriber that could never run correctly over a stream
// of partitions partitions.
func (c *SubscriberConfig) validate(partitions int) error {
	//: the refusals that need no stream, in the order a reader fills them in.
	if field := c.missing(); field != "" {
		//: SubscriberMisconfigured, naming the field.
		return subscriberMisconfigured(field)
	}
	seen := make(map[int]bool, len(c.Partitions))
	//: every partition named is one of the stream's, once.
	for _, partition := range c.Partitions {
		//: outside the stream, or a second worker for one partition.
		if partition < 0 || partition >= partitions || seen[partition] {
			//: SubscriberMisconfigured, with the partition and the count.
			return errs.Wrap(corestream.SubscriberMisconfigured, errs.WrapParams{},
				errs.String("field", "Partitions"), errs.Int("partition", partition), errs.Int("partitions", partitions))
		}
		seen[partition] = true
	}
	//: runnable.
	return nil
}

// missing names the first required field c lacks, or returns "".
func (c *SubscriberConfig) missing() string {
	//: the first field missing, in the order a reader fills them in.
	switch {
	//: a nil handler reads records and drops them.
	case c.Handler == nil:
		return "Handler"
	//: a cursor belongs to a named group.
	case c.Group == "":
		return "Group"
	//: THE assertion: its zero is the conservative reading.
	case !c.HandlerIsIdempotent:
		return "HandlerIsIdempotent"
	//: two modes, and no third the subscriber would guess at.
	case c.Commit != CommitAfterHandler && c.Commit != CommitByHandler:
		return "Commit"
	default:
		return c.missingCollaborator()
	}
}

// missingCollaborator names the first of the fence's and the park's
// requirements c lacks, or returns "".
func (c *SubscriberConfig) missingCollaborator() string {
	//: the first field missing, in the order a reader fills them in.
	switch {
	//: a fence's lock names the stream.
	case c.Fence != nil && c.Name == "":
		return "Name"
	//: a park needs somewhere to park.
	case c.Park != nil && c.Park.Broker == nil:
		return "Park.Broker"
	//: nothing missing.
	default:
		return ""
	}
}

// normalized resolves the clamped fields, the partitions included, so every
// worker reads the same numbers.
func (c *SubscriberConfig) normalized(partitions int) SubscriberConfig {
	out := *c
	out.BatchSize = max(out.BatchSize, 1)
	//: zero is the request for the default cadence.
	if out.PollInterval <= 0 {
		out.PollInterval = subscriberPollInterval
	}
	//: zero asks for the default bound; nothing is sent unbounded past a stop.
	if out.SettleTimeout <= 0 {
		out.SettleTimeout = subscriberSettleTimeout
	}
	//: a curve with no wait would spin on a failing handler.
	if out.Retry.BaseDelay <= 0 {
		out.Retry = backoff.Value{
			BaseDelay: retryBase, MaxDelay: retryCeiling, Multiplier: retryMultiplier, Jitter: retryJitter,
		}
	}
	//: nil is the caller with no opinion.
	if out.Clock == nil {
		out.Clock = clock.System
	}
	//: a copy, so the caller's park is not edited.
	if out.Park != nil {
		park := *out.Park
		park.After = max(park.After, 1)
		out.Park = &park
	}
	//: empty is every partition.
	if len(out.Partitions) == 0 {
		out.Partitions = make([]int, 0, partitions)
		//: each partition, in order.
		for i := range partitions {
			out.Partitions = append(out.Partitions, i)
		}
	}
	//: the values the workers run on.
	return out
}
