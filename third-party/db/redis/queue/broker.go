package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The stream entry's fields: the payload, and the enqueue instant in Unix
// nanoseconds on the broker's clock.
const (
	fieldPayload  string = "p"
	fieldEnqueued string = "e"
)

// reclaimLimit bounds how many lapsed leases one Receive reclaims, so one
// script never holds the server for a backlog of dead consumers.
const reclaimLimit int = 256

// randomBytes is the length of a minted consumer name or token base.
const randomBytes int = 8

// noGroup is what receiveScript answers when the group does not exist.
const noGroup string = "NOGROUP"

// streamBroker is the broker NewStream returns.
type streamBroker struct {
	client   goredis.UniversalClient
	clock    clock.Clock
	waker    *waker
	keys     []string
	group    string
	consumer string
	policy   corequeue.PolicyValue
	create   bool
}

// NewStream returns a broker over cfg.Stream and cfg.Group. It sends no
// command: the group is checked, or created when cfg.CreateGroup asks, on
// the first Receive. A policy the broker cannot honour is
// core/data/queue.QueueMisconfigured; a missing client or name is
// StreamMisconfigured.
func NewStream(cfg StreamConfig) (broker corequeue.Broker, err error) {
	//: everything decidable without the server is decided here.
	if verr := cfg.validate(); verr != nil {
		//: nothing built.
		return nil, verr
	}
	consumer := cfg.Consumer
	//: two live brokers must not share a name: mint one.
	if consumer == "" {
		consumer = "consumer-" + randomHex()
	}
	tag := "{" + cfg.Stream + "}:"
	//: no command sent.
	return &streamBroker{
		client: cfg.Client, clock: cfg.clockOrSystem(), waker: newWaker(),
		keys:  []string{tag + "stream", tag + "lease", tag + "wait", tag + "count", tag + "token", tag + "dead"},
		group: cfg.Group, consumer: consumer, policy: cfg.Policy.Normalized(), create: cfg.CreateGroup,
	}, nil
}

// micros is t as the scripts read an instant: Unix microseconds, as text.
func micros(t time.Time) string {
	//: decimal, never an exponent.
	return strconv.FormatInt(t.UnixMicro(), 10)
}

// randomHex is a random name part.
func randomHex() string {
	raw := make([]byte, randomBytes)
	_, _ = rand.Read(raw)
	//: crypto/rand never fails on the platforms Go supports.
	return hex.EncodeToString(raw)
}

// Publish appends payload to the stream: one XADD.
func (b *streamBroker) Publish(ctx context.Context, payload []byte) (corequeue.MessageValue, error) {
	//: the policy's bound.
	if len(payload) > b.policy.MaxMessageBytes {
		//: refused at the call site that built it.
		return corequeue.MessageValue{}, errs.Wrap(corequeue.MessageTooLarge, errs.WrapParams{},
			errs.Int("bytes", len(payload)), errs.Int("max", b.policy.MaxMessageBytes))
	}
	now := b.clock.Now()
	id, aerr := b.client.XAdd(ctx, &goredis.XAddArgs{
		Stream: b.keys[0], Values: []any{fieldPayload, payload, fieldEnqueued, now.UnixNano()},
	}).Result()
	//: nothing published.
	if aerr != nil {
		//: STREAM_UNREACHABLE or COMMAND_REFUSED.
		return corequeue.MessageValue{}, commandError(aerr, "XADD")
	}
	b.waker.signal(now)
	//: stored as the server's persistence stores it.
	return corequeue.MessageValue{ID: id, Payload: slices.Clone(payload), EnqueuedAt: now}, nil
}

// Receive leases up to max messages: one script, retried once after the
// group is created when CreateGroup asks for it.
func (b *streamBroker) Receive(ctx context.Context, max int) ([]corequeue.DeliveryValue, error) {
	//: a batch is positive.
	if max <= 0 {
		//: INVALID_BATCH_SIZE.
		return nil, errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	now := b.clock.Now()
	deadline := now.Add(b.policy.VisibilityTimeout)
	rows, missing, rerr := b.receive(ctx, max, now, deadline)
	//: the group is missing: created on request, refused otherwise.
	if rerr == nil && missing {
		//: GROUP_MISSING, or the creation's failure.
		if cerr := b.createGroup(ctx); cerr != nil {
			return nil, cerr
		}
		rows, _, rerr = b.receive(ctx, max, now, deadline)
	}
	//: the script failed.
	if rerr != nil {
		//: STREAM_UNREACHABLE or COMMAND_REFUSED.
		return nil, rerr
	}
	deliveries := b.deliveries(rows, deadline)
	b.waker.schedule(deadline, len(deliveries))
	//: possibly empty: Receive does not wait.
	return deliveries, nil
}

// receive runs receiveScript once, and returns the leased entries it
// answered, or missing when it answered that the group does not exist.
func (b *streamBroker) receive(
	ctx context.Context, max int, now, deadline time.Time,
) (rows []any, missing bool, err error) {
	reply, rerr := receiveScript.Run(ctx, b.client, b.keys,
		micros(now), micros(deadline), max, b.group, b.consumer, b.policy.MaxDeliveries, randomHex()+"-", reclaimLimit,
	).Result()
	//: the script failed.
	if rerr != nil {
		//: typed.
		return nil, false, commandError(rerr, "RECEIVE")
	}
	//: NOGROUP, or the leased entries.
	switch answer := reply.(type) {
	case []any:
		//: the leased entries, possibly none.
		return answer, false, nil
	case string:
		//: NOGROUP, or a word the script never answers.
		return nil, answer == noGroup, nil
	default:
		//: nothing the script returns: no entry.
		return nil, false, nil
	}
}

// createGroup creates the group and the stream with it, or refuses.
func (b *streamBroker) createGroup(ctx context.Context) error {
	//: the caller did not ask for it.
	if !b.create {
		//: GROUP_MISSING.
		return errs.Wrap(GroupMissing, errs.WrapParams{}, errs.String("group", b.group))
	}
	cerr := b.client.XGroupCreateMkStream(ctx, b.keys[0], b.group, "0").Err()
	//: created, or created meanwhile by another broker.
	if cerr == nil || goredis.HasErrorPrefix(cerr, "BUSYGROUP") {
		return nil
	}
	//: typed.
	return commandError(cerr, "XGROUP CREATE")
}

// deliveries reads the leased entries the script returned.
func (b *streamBroker) deliveries(rows []any, deadline time.Time) []corequeue.DeliveryValue {
	out := make([]corequeue.DeliveryValue, 0, len(rows))
	//: each {id, fields, deliveries, token}.
	for _, row := range rows {
		item, ok := row.([]any)
		//: anything else is not what the script returns.
		if !ok || len(item) != 4 {
			continue
		}
		id, _ := item[0].(string)
		count, _ := item[2].(int64)
		token, _ := item[3].(string)
		receipt := receiptValue{id: id, token: token, deliveries: int(count)}.encode()
		out = append(out, corequeue.DeliveryValue{
			Lease:   corequeue.LeaseValue{ExpiresAt: deadline, Receipt: receipt},
			Message: decodeEntry(id, fieldsOf(item[1])), Deliveries: int(count),
		})
	}
	//: in the script's order: redeliveries first, then new entries.
	return out
}

// fieldsOf reads a stream entry's field list: field, value, field, value…;
// none when the reply holds no list.
func fieldsOf(reply any) []any {
	//: a list, or nothing the script returns.
	switch fields := reply.(type) {
	case []any:
		//: as the stream holds them.
		return fields
	default:
		//: no field.
		return nil
	}
}

// decodeEntry reads a stream entry's fields back into the message. A message
// with no payload field reads as an empty payload, never a nil one.
func decodeEntry(id string, fields []any) corequeue.MessageValue {
	message := corequeue.MessageValue{ID: id, Payload: []byte("")}
	//: field, value, field, value…
	for i := 0; i+1 < len(fields); i += 2 {
		name, _ := fields[i].(string)
		value, _ := fields[i+1].(string)
		//: the two fields Publish writes; any other is not ours.
		switch name {
		case fieldPayload:
			message.Payload = []byte(value)
		case fieldEnqueued:
			message.EnqueuedAt = time.Unix(0, unixNanosOf(value))
		}
	}
	//: as published.
	return message
}

// unixNanosOf reads the enqueue instant Publish writes, in Unix nanoseconds:
// absent, or not a number, is zero; one out of range is the bound it passed,
// as ParseInt reads it.
func unixNanosOf(value string) int64 {
	nanos, perr := strconv.ParseInt(value, 10, 64)
	//: not a number at all.
	if errors.Is(perr, strconv.ErrSyntax) {
		//: the epoch.
		return 0
	}
	//: the instant written.
	return nanos
}
