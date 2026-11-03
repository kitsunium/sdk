package queue

import (
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// StreamConfig configures NewStream: the client, the stream and group, this
// process's consumer name, and the delivery discipline.
type StreamConfig struct {
	// Client is the caller's go-redis client; the broker never closes it.
	// Required.
	Client goredis.UniversalClient
	// Clock stamps every instant the broker writes and judges the lease
	// deadlines. Nil means clock.System.
	Clock clock.Clock
	// Stream names the queue; it is the hash tag of every key the broker
	// writes, so it must be non-empty and hold no brace. Required.
	Stream string
	// Group is the consumer group every consumer of the queue reads through. Required.
	Group string
	// Consumer names this broker in the group's pending-entries list. Empty
	// mints one per broker; two live brokers must not share one.
	Consumer string
	// Policy is the delivery discipline. Its zero value is refused, as for
	// every broker.
	Policy corequeue.PolicyValue
	// CreateGroup creates the group, and the stream with it (XGROUP CREATE
	// MKSTREAM), when the first Receive finds it missing. False answers
	// GroupMissing instead.
	CreateGroup bool
}

// validate refuses, by field, a StreamConfig no broker can be built from.
func (c *StreamConfig) validate() error {
	//: the policy first: its refusals are the domain's own.
	if perr := c.Policy.Validate(); perr != nil {
		//: QUEUE_MISCONFIGURED, naming the policy's field.
		return perr
	}
	//: the first field that is wrong, in declaration order.
	switch {
	case c.Client == nil:
		//: named.
		return misconfigured("Client")
	//: the stream is the hash tag: a brace would end it early.
	case c.Stream == "" || strings.ContainsAny(c.Stream, "{}"):
		//: named.
		return misconfigured("Stream")
	case c.Group == "":
		//: named.
		return misconfigured("Group")
	default:
		//: a broker can be built.
		return nil
	}
}

// misconfigured refuses the configuration, naming the field at fault.
func misconfigured(field string) error {
	//: origin wins: the sentinel's identity, the field as a diagnostic.
	return errs.Wrap(StreamMisconfigured, errs.WrapParams{}, errs.String("field", field))
}

// clockOrSystem is the configured clock, the system's when none is.
func (c *StreamConfig) clockOrSystem() clock.Clock {
	//: nil means the wall clock.
	if c.Clock == nil {
		//: package time.
		return clock.System
	}
	//: the caller's, a test's ManualClock included.
	return c.Clock
}
