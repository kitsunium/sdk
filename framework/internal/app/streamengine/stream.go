package streamengine

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	sdkstream "github.com/kitsunium/sdk/pkg/v1/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A stream as kit's core holds it (plug.Stream): one of the SDK's streams, the
// clock its groups wait on, and the conversions between the SDK's values and
// plug's.

// stream is one of the SDK's streams as kit's core uses it.
type stream struct {
	s     sdkstream.Stream
	clock clock.Timed
	name  string
}

// Append puts payload in the stream under key, one record of one batch: one
// sync on a file stream, one statement in the transaction ctx carries on a
// SQL one.
func (s *stream) Append(ctx context.Context, key string, payload []byte) error {
	_, err := s.s.Append(ctx, []sdkstream.Record{{Key: []byte(key), Payload: payload}})
	return err
}

// Subscribe runs the consumer group cfg names until ctx ends or the stream's
// storage fails: a group the stream does not know yet is first placed where
// cfg starts it (Join), then the SDK's subscriber hands cfg's handler each record
// of each partition in offset order, at most cfg.Parallelism at once, and
// commits past it once the handler accepted it. Once ctx ended no record
// starts; the one running may run cfg.DrainTimeout more.
//
// Goroutine lifecycle: the goroutine started here ends the subscriber's
// context once the drain after ctx's end is over, or once Subscribe
// returned, whichever comes first.
func (s *stream) Subscribe(ctx context.Context, cfg plug.StreamSubscribeConfig) error {
	if err := s.Join(ctx, cfg); err != nil {
		return err
	}
	inner, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	run := &running{idle: make(chan struct{}, 1)}
	done := make(chan struct{})
	defer close(done)
	go s.drainAfter(ctx, done, cfg.DrainTimeout, run, cancel)
	slots := make(chan struct{}, max(cfg.Parallelism, 1))
	handle := cfg.Handler
	return sdkstream.Subscribe(inner, s.s, sdkstream.SubscriberConfig{
		Group:               cfg.Group,
		Name:                s.name,
		HandlerIsIdempotent: true,
		Clock:               s.clock,
		PollInterval:        cfg.PollInterval,
		SettleTimeout:       cfg.SettleTimeout,
		Handler: func(hctx context.Context, entries []sdkstream.Entry) error {
			for i := range entries {
				select {
				case slots <- struct{}{}:
				case <-hctx.Done():
					return hctx.Err()
				}
				err := run.handle(ctx, func() error { return handle(hctx, pluggedRecord(&entries[i])) })
				<-slots
				if err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// running counts the records being handled, so that a stop waits for them
// alone: idle is signalled each time one ends.
type running struct {
	idle chan struct{}
	mu   sync.Mutex
	busy int
}

// handle runs fn as one record's handling, unless stop ended: a record does
// not start once the app began to stop — refused, it is read again at the
// next start.
func (r *running) handle(stop context.Context, fn func() error) error {
	r.mu.Lock()
	if err := stop.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.busy++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.busy--
		r.mu.Unlock()
		select {
		case r.idle <- struct{}{}:
		default:
		}
	}()
	return fn()
}

// quiet reports whether no record is being handled.
func (r *running) quiet() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy == 0
}

// drainAfter cancels the subscriber once ctx ended and no record is being
// handled — at the latest drain after ctx ended —, or once done is closed.
func (s *stream) drainAfter(ctx context.Context, done <-chan struct{}, drain time.Duration, run *running, cancel context.CancelFunc) {
	defer cancel()
	select {
	case <-ctx.Done():
	case <-done:
		return
	}
	var deadline <-chan time.Time
	if s.clock != nil {
		deadline = s.clock.After(max(drain, 0))
	} else {
		deadline = time.After(max(drain, 0))
	}
	for !run.quiet() {
		select {
		case <-run.idle:
		case <-deadline:
			return
		case <-done:
			return
		}
	}
}

// Join places a group the stream does not know yet where cfg starts it,
// on every partition: after the last record appended (the default), at the
// first appended at or after cfg.From, or — FromBeginning — nowhere, the
// SDK's subscriber starting it at the first record kept. A group the stream
// knows keeps its offsets.
func (s *stream) Join(ctx context.Context, cfg plug.StreamSubscribeConfig) error {
	if cfg.FromBeginning {
		return nil
	}
	committer, ok := s.s.(sdkstream.Committer)
	if !ok {
		return lacks(s.name, "Committer")
	}
	for p := range s.s.Partitions() {
		_, err := committer.Committed(ctx, cfg.Group, p)
		if err == nil {
			continue
		}
		if !errs.HasCode(err, sdkstream.CodeUnknownGroup) {
			return err
		}
		start, err := s.startOf(ctx, p, cfg.From)
		if err != nil {
			return err
		}
		if err := committer.Commit(ctx, cfg.Group, p, start); err != nil {
			return err
		}
	}
	return nil
}

// startOf is the offset a new group starts at on partition: the first
// appended at or after from, or — from zero — the next to be appended.
func (s *stream) startOf(ctx context.Context, partition int, from time.Time) (sdkstream.Offset, error) {
	if !from.IsZero() {
		seeker, ok := s.s.(sdkstream.Seeker)
		if !ok {
			return 0, lacks(s.name, "Seeker")
		}
		return seeker.OffsetAt(ctx, partition, from)
	}
	bounds, err := s.s.Bounds(ctx, partition)
	return bounds.Next, err
}

// Close releases the stream: a file stream's directory.
func (s *stream) Close() error {
	if c, ok := s.s.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// pluggedRecord is the SDK's entry as kit's core reads it.
func pluggedRecord(e *sdkstream.Entry) plug.StreamRecordValue {
	return plug.StreamRecordValue{
		AppendedAt: e.AppendedAt, Key: string(e.Record.Key), Payload: e.Record.Payload,
		Partition: e.Partition, Offset: int64(e.Offset),
	}
}

// lacks is the refusal of a stream that does not answer a sibling every
// engine of the SDK's answers: one a test's wrapper hid.
func lacks(name, sibling string) error {
	return errs.Wrap(sdkstream.StreamMisconfigured, errs.WrapParams{},
		errs.String("stream", name), errs.String("sibling", sibling))
}
