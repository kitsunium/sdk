package queueengine

import (
	"io"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// : Asserts at compile time that a queue is what plug describes, with every
// sibling kit's core asks for, that a mailer's sealed outbox answers every
// sibling the SDK's SQL broker does, and that the app's clock is the SDK's.
var (
	_ plug.Broker       = (*broker)(nil)
	_ plug.MessageMover = (*broker)(nil)
	_ io.Closer         = (*broker)(nil)

	_ queue.Waker             = (*sealedOutbox)(nil)
	_ queue.LeaseExtender     = (*sealedOutbox)(nil)
	_ queue.DeadLetterReader  = (*sealedOutbox)(nil)
	_ queue.Rejecter          = (*sealedOutbox)(nil)
	_ queue.DeadLetterManager = (*sealedOutbox)(nil)
	_ queue.MessageReader     = (*sealedOutbox)(nil)
	_ queue.MessageWriter     = (*sealedOutbox)(nil)
	_ queue.DeadLetterWriter  = (*sealedOutbox)(nil)

	_ clock.Timed = sdkClock{}
)
