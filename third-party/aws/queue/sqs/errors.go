package sqs

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// requestFailed types a failed SQS request: the package's sentinel the
// request is — given by its Params —, QUEUE_BACKEND_FAILED in its trail, the
// AWS error in the chain.
func requestFailed(cause error, sentinel errs.WrapParams, op string) error {
	coded := errs.Wrap(cause, sentinel, errs.String("op", op))
	//: origin wins: the package's code stays, the domain's joins the trail.
	return errs.Wrap(coded, corequeue.QueueBackendFailed.Params())
}

// delayTooLong refuses a delay SQS cannot carry; nothing moved.
func delayTooLong(field string, delay, ceiling int64) error {
	//: the asked delay and the ceiling, in nanoseconds.
	return errs.Wrap(DelayTooLong, errs.WrapParams{},
		errs.String("field", field), errs.Int64("delay_ns", delay), errs.Int64("max_ns", ceiling))
}
