package queue

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The headers a dead letter carries besides headerEnqueued: what
// DeadLetterValue holds besides the payload.
const (
	headerID         string = "Kitsunium-Id"
	headerFailed     string = "Kitsunium-Failed"
	headerReason     string = "Kitsunium-Reason"
	headerCause      string = "Kitsunium-Cause"
	headerCode       string = "Kitsunium-Code"
	headerTrail      string = "Kitsunium-Trail"
	headerDeliveries string = "Kitsunium-Deliveries"
)

// trailSep joins a trail's codes in headerTrail.
const trailSep string = ","

// deadLetter publishes lease's message on the dead-letter subject with
// cause, then terminates it. The two are not atomic: a crash between them
// redelivers the message, which is dead-lettered again — a duplicate dead
// letter, never a lost message.
func (b *jetStreamBroker) deadLetter(ctx context.Context, lease *leaseValue, cause causeValue) error {
	msg := nats.NewMsg(b.cfg.DeadLetterSubject)
	msg.Data = lease.message.Payload
	setHeaders(msg, lease, cause, b.clock.Now())
	pctx, cancel := b.call(ctx)
	defer cancel()
	//: nothing moved: the message stays leased, then redelivered.
	if _, perr := b.js.PublishMsg(pctx, msg, jetstream.WithExpectStream(b.cfg.Stream)); perr != nil {
		return publishError(perr)
	}
	//: dead-lettered, and still in the work queue: the duplicate stated above.
	if terr := lease.msg.Term(); terr != nil {
		return requestError(terr, "term")
	}
	//: moved.
	return nil
}

// setHeaders writes in msg's headers what a dead letter keeps of its message
// and its cause.
func setHeaders(msg *nats.Msg, lease *leaseValue, cause causeValue, failed time.Time) {
	header := msg.Header
	header.Set(headerEnqueued, strconv.FormatInt(lease.message.EnqueuedAt.UnixNano(), decimal))
	header.Set(headerID, lease.message.ID)
	header.Set(headerFailed, strconv.FormatInt(failed.UnixNano(), decimal))
	header.Set(headerReason, cause.reason)
	header.Set(headerCause, cause.public)
	header.Set(headerCode, strconv.FormatUint(uint64(cause.code), decimal))
	header.Set(headerTrail, encodeTrail(cause.trail))
	header.Set(headerDeliveries, strconv.Itoa(lease.deliveries))
}

// DeadLetters returns up to max dead letters, oldest first, without removing
// any: a direct read of the stream by subject, from its first sequence.
func (b *jetStreamBroker) DeadLetters(ctx context.Context, max int) ([]corequeue.DeadLetterValue, error) {
	//: a batch is positive.
	if max <= 0 {
		return nil, errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	//: the stream is looked up first.
	if _, rerr := b.ready(ctx); rerr != nil {
		return nil, rerr
	}
	dead := make([]corequeue.DeadLetterValue, 0, min(max, 64))
	next := uint64(1)
	//: the next dead letter at or after next, until max or the end.
	for len(dead) < max {
		gctx, cancel := b.call(ctx)
		raw, gerr := b.stream.GetMsg(gctx, next, jetstream.WithGetMsgSubject(b.cfg.DeadLetterSubject))
		cancel()
		//: no dead letter beyond.
		if errors.Is(gerr, jetstream.ErrMsgNotFound) {
			break
		}
		//: the read failed.
		if gerr != nil {
			return nil, requestError(gerr, "get")
		}
		dead = append(dead, decodeDeadLetter(raw))
		next = raw.Sequence + 1
	}
	//: oldest first: the stream's order.
	return dead, nil
}

// decodeDeadLetter reads back what setHeaders wrote.
func decodeDeadLetter(raw *jetstream.RawStreamMsg) corequeue.DeadLetterValue {
	number := func(name string) int64 {
		value, perr := strconv.ParseInt(raw.Header.Get(name), decimal, 64)
		//: absent, or not a number, is zero; one out of range is the bound
		//: it passed, as ParseInt reads it.
		if errors.Is(perr, strconv.ErrSyntax) {
			return 0
		}
		return value
	}
	code, codeErr := strconv.ParseUint(raw.Header.Get(headerCode), decimal, 32)
	//: absent, or not a number, is no code.
	if errors.Is(codeErr, strconv.ErrSyntax) {
		code = 0
	}
	//: every field of the record.
	return corequeue.DeadLetterValue{
		FailedAt: time.Unix(0, number(headerFailed)), Reason: raw.Header.Get(headerReason),
		Cause: raw.Header.Get(headerCause), Code: uint32(code), Trail: decodeTrail(raw.Header.Get(headerTrail)),
		Deliveries: int(number(headerDeliveries)),
		Message: corequeue.MessageValue{
			ID: raw.Header.Get(headerID), Payload: bytes.Clone(raw.Data), EnqueuedAt: time.Unix(0, number(headerEnqueued)),
		},
	}
}

// encodeTrail writes a trail's codes in decimal, oldest first.
func encodeTrail(trail []uint32) string {
	parts := make([]string, len(trail))
	//: each code in decimal.
	for i, code := range trail {
		parts[i] = strconv.FormatUint(uint64(code), decimal)
	}
	//: "" for none.
	return strings.Join(parts, trailSep)
}

// decodeTrail reads back encodeTrail; a trail it cannot read whole is none.
func decodeTrail(text string) []uint32 {
	//: none written.
	if text == "" {
		return nil
	}
	parts := strings.Split(text, trailSep)
	trail := make([]uint32, 0, len(parts))
	//: each code a uint32, or the whole trail is dropped.
	for _, part := range parts {
		code, perr := strconv.ParseUint(part, decimal, 32)
		//: a corrupt field costs its own value only.
		if perr != nil {
			return nil
		}
		trail = append(trail, uint32(code))
	}
	//: oldest first.
	return trail
}
