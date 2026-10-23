package otlpengine

import (
	"context"
	"errors"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/forward"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The export's dead letters as the product's CLI reads them (issue #536):
// one signal's queue opened as open keeps it — the same directory or table,
// the same policy (openQueue) —, its dead letters listed without their
// payload, replayed into the live queue or dropped, through the SDK queue's
// DeadLetterReader and DeadLetterManager siblings. Never a route (D13).

// deadLetters is one signal's queue, open for its dead letters.
type deadLetters struct {
	broker queue.Broker
}

// openDeadLetters opens signal's queue as the export keeps it; a queue
// that would be in memory is refused: no dead letter outlives its process.
//
// IFACE-PLUGIN: the framework reaches the queue through plug.OTLPQueue;
// this is its implementation.
func openDeadLetters(_ context.Context, cfg *plug.OTLPConfig, signal string) (plug.OTLPQueue, error) {
	if signal != plug.OTLPTraces && signal != plug.OTLPMetrics {
		return nil, errs.Wrap(forward.Misconfigured, errs.WrapParams{}, errs.String("signal", signal), errs.String("problem", "no such signal"))
	}
	b, durable, err := openQueue(cfg, clockOf(cfg.Clock), signal)
	if err != nil {
		return nil, err
	}
	if !durable {
		return nil, errors.Join(errs.Wrap(forward.Misconfigured, errs.WrapParams{},
			errs.String("signal", signal), errs.String("problem", "the queue is in memory")), closeQueue(b))
	}
	return &deadLetters{broker: b}, nil
}

// DeadLetters are at most max of the queue's dead letters, oldest first,
// their payload's size in place of the payload.
func (q *deadLetters) DeadLetters(ctx context.Context, max int) ([]plug.OTLPDeadLetterValue, error) {
	reader, ok := q.broker.(queue.DeadLetterReader)
	if !ok {
		return nil, lacksDeadLetters()
	}
	dead, err := reader.DeadLetters(ctx, max)
	if err != nil {
		return nil, err
	}
	out := make([]plug.OTLPDeadLetterValue, 0, len(dead))
	for _, d := range dead {
		out = append(out, plug.OTLPDeadLetterValue{
			FailedAt: d.FailedAt, ID: d.Message.ID, Reason: d.Reason, Cause: d.Cause, Trail: d.Trail,
			Code: d.Code, Deliveries: d.Deliveries, Size: len(d.Message.Payload),
		})
	}
	return out, nil
}

// Replay moves the dead letter id back into the live queue.
func (q *deadLetters) Replay(ctx context.Context, id string) (bool, error) {
	manager, ok := q.broker.(queue.DeadLetterManager)
	if !ok {
		return false, lacksDeadLetters()
	}
	return found(manager.ReplayDeadLetter(ctx, id))
}

// Drop deletes the dead letter id for good.
func (q *deadLetters) Drop(ctx context.Context, id string) (bool, error) {
	manager, ok := q.broker.(queue.DeadLetterManager)
	if !ok {
		return false, lacksDeadLetters()
	}
	return found(manager.DeleteDeadLetter(ctx, id))
}

// Close releases what the queue holds.
func (q *deadLetters) Close() error { return closeQueue(q.broker) }

// found is err as the CLI reads it: a dead letter the queue does not hold
// is not found, and no failure.
func found(err error) (bool, error) {
	if errors.Is(err, queue.DeadLetterNotFound) {
		return false, nil
	}
	return err == nil, err
}

// lacksDeadLetters is the refusal of a queue that keeps no dead letter.
func lacksDeadLetters() error {
	return errs.Wrap(forward.Misconfigured, errs.WrapParams{}, errs.String("problem", "the queue keeps no dead letter"))
}
