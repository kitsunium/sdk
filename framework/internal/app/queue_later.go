package app

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A message for later, and a message once (issue #483, part 3, ADR 0183):
// [TopicService.PublishAt] and [Command.DispatchAt] put a message no consumer
// receives before an instant — the SDK's queue.DelayedPublisher —, and a
// topic that declares a key ([TopicService.Once]) publishes each message at
// most once per key over a window, its setting <service>-topic-<name>-once-window
// — the SDK's queue.OncePublisher. Inside a transaction either is held to
// the commit as a publication now is (ADR 0178): joined to the transaction
// when the queue lives on its database, put once it committed otherwise.

// knobOnceWindow is the knob of a topic's deduplication window.
const knobOnceWindow = "once-window"

// publication is how a message goes into a queue: now, at an instant, or
// once per key over a window.
type publication struct {
	at     time.Time
	key    string
	window time.Duration
}

// into puts payload in b as the publication says, and reports whether it
// did: false for a message a publication under its key put there within its
// window.
func (p publication) into(ctx context.Context, b plug.Broker, payload []byte) (bool, error) {
	if p.at.IsZero() && p.key == "" {
		return true, b.Publish(ctx, payload)
	}
	later, ok := b.(plug.LaterPublisher)
	if !ok {
		return false, Unavailable("the queue cannot publish for later or once")
	}
	if p.key != "" {
		return later.PublishOnce(ctx, p.key, payload, p.window)
	}
	return true, later.PublishAt(ctx, payload, p.at)
}

// mark says on the span of a publication what made it other than now.
func (p publication) mark(sp *span) {
	if !p.at.IsZero() {
		sp.attr("at", p.at.UTC().Format(time.RFC3339Nano))
	}
	if p.key != "" {
		sp.attr("instance", clip(p.key))
	}
}

// laterOnce refuses a publication for later on a topic that publishes once:
// the SDK has no publication once for later (ADR 0183).
func laterOnce(t *nodeBase) error {
	return failure(CodeTopicPublish, "TOPIC_LATER_ONCE", "a topic that publishes once cannot publish for later", nil,
		errs.String("topic", t.id))
}

// noTopicKey refuses a message whose key is empty on a topic that publishes
// once.
func noTopicKey(t *nodeBase) error {
	return failure(CodeTopicPublish, "TOPIC_KEY_EMPTY", "the message's key is empty", nil, errs.String("topic", t.id))
}

// notQueued refuses a dispatch for later of a command that is not queued:
// only a queue holds a dispatch until its instant.
func notQueued(c *nodeBase) error {
	return failure(CodeCommandQueue, "COMMAND_NOT_QUEUED", "only a queued command is dispatched for later", nil,
		errs.String("command", c.id))
}
