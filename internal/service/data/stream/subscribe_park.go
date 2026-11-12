package stream

import (
	"context"
	"encoding/json"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// narrowOrPark acts on a batch refused Park.After times: a batch of several
// records is narrowed to its first, so the record that fails is found one
// at a time; a single record is parked and the group's offset committed
// past it.
func (w *subscriber) narrowOrPark(ctx context.Context, c *cursor, entries []corestream.EntryValue) error {
	//: several records: read them one at a time until past this batch.
	if len(entries) > 1 {
		c.narrowUntil = entries[len(entries)-1].Offset + 1
		c.failures = 0
		//: the next turn delivers the first record alone, at once.
		return nil
	}
	err := w.park(ctx, entries[0])
	//: not parked: the record stays where it is.
	if err != nil {
		//: the queue's refusal.
		return err
	}
	c.failures = 0
	//: committed in both modes: the handler never accepted this record, so
	//: nothing else will move the group past it.
	return w.advance(ctx, c, entries[0].Offset+1)
}

// park publishes entry to the park's queue as a dead letter: its JSON.
func (w *subscriber) park(ctx context.Context, entry corestream.EntryValue) error {
	payload, err := json.Marshal(entry)
	//: unreachable for an EntryValue, whose fields all encode.
	if err != nil {
		//: StreamBackendFailed, naming the operation.
		return errs.Wrap(corestream.StreamBackendFailed, errs.WrapParams{}, errs.String("op", "encode a dead letter"))
	}
	//: bounded past a stop, as every outcome is.
	return w.settled(ctx, func(settleCtx context.Context) error {
		_, publishErr := w.cfg.Park.Broker.Publish(settleCtx, payload)
		//: the queue's own refusal, as it gave it.
		return publishErr
	})
}
