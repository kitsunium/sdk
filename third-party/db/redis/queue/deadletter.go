package queue

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// DeadLetters returns up to max dead letters, oldest first, without removing
// any: one XRANGE of the dead-letter stream.
func (b *streamBroker) DeadLetters(ctx context.Context, max int) ([]corequeue.DeadLetterValue, error) {
	//: a batch is positive.
	if max <= 0 {
		//: INVALID_BATCH_SIZE.
		return nil, errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	entries, rerr := b.client.XRangeN(ctx, b.keys[5], "-", "+", int64(max)).Result()
	//: nothing read.
	if rerr != nil {
		//: typed.
		return nil, commandError(rerr, "XRANGE")
	}
	dead := make([]corequeue.DeadLetterValue, 0, len(entries))
	//: each record, in the order it was written.
	for _, entry := range entries {
		dead = append(dead, decodeDeadLetter(entry.Values))
	}
	//: oldest first.
	return dead, nil
}

// decodeDeadLetter reads back a record the dead-letter script wrote.
func decodeDeadLetter(values map[string]any) corequeue.DeadLetterValue {
	text := func(name string) string {
		value, _ := values[name].(string)
		//: absent is empty.
		return value
	}
	number := func(name string) int64 {
		value, perr := strconv.ParseInt(text(name), decimal, 64)
		//: absent, or not a number, is zero; one out of range is the bound
		//: it passed, as ParseInt reads it.
		if errors.Is(perr, strconv.ErrSyntax) {
			return 0
		}
		return value
	}
	failed := time.UnixMicro(number("f"))
	code, codeErr := strconv.ParseUint(text("k"), decimal, 32)
	//: absent, or not a number, is no code.
	if errors.Is(codeErr, strconv.ErrSyntax) {
		code = 0
	}
	//: every field of the record.
	return corequeue.DeadLetterValue{
		FailedAt: failed, Reason: text("r"), Cause: text("c"), Code: uint32(code),
		Deliveries: int(number("n")), Trail: decodeTrail(text("t")),
		Message: corequeue.MessageValue{
			ID: text("id"), Payload: []byte(text(fieldPayload)), EnqueuedAt: time.Unix(0, number(fieldEnqueued)),
		},
	}
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
