package app

import (
	"slices"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A handler that says when to try again (issue #483, part 3): [RetryIn]
// marks its failure with a delay, and the consumer that runs it — a
// subscription's, a watch's, a queued command's — reads it (retryInOf, the
// plug.ConsumeConfig's RetryIn) and the queue subsystem hands its message
// back with that delay — the SDK's queue.RetryAfter, its broker's NackAfter —
// instead of waiting consumerPolicy's growing delay. The delivery is
// counted, so a message whose attempts are spent is dead-lettered all the
// same; a failure no retry can help wins over it.

// retryInField names the field RetryIn carries its delay in, as integer
// nanoseconds; retryInOf is its one reader.
const retryInField string = "retry_in_ns"

// RetryIn marks cause as a failure to retry in d, no sooner: returned from
// the handler of a subscription, a watch or a queued command, it hands the
// message back to its queue with that delay, in place of the queue's own —
// half a second after a first failure, doubling, never more than a minute —:
// a downstream that answered with a Retry-After, a resource that frees at a
// known instant. The delivery counts as an attempt: a message that used its
// [MaxDeliveries] is dead-lettered with cause, the delay unused. A negative
// d is read as zero.
//
//	if resp.StatusCode == http.StatusTooManyRequests {
//		return app.RetryIn(30*time.Second, errThrottled)
//	}
//
// cause keeps its own code, reason and words — what a dead letter records —
// and [CodeRetryIn] joins its trail; a cause that is no error of the SDK's,
// or none, is recorded as CodeRetryIn itself. Outside a consumer's handler
// it is an error like any other.
func RetryIn(d time.Duration, cause error) error {
	return failure(CodeRetryIn, "RETRY_IN", "the message is handed back to be retried later", cause,
		errs.Int64(retryInField, int64(max(d, 0))))
}

// retryInOf is the delay RetryIn put on err, and whether it put one: the
// outermost mark's when err was marked more than once.
func retryInOf(err error) (time.Duration, bool) {
	if !errs.HasCode(err, CodeRetryIn) {
		return 0, false
	}
	for _, f := range slices.Backward(errs.FieldsOf(err)) {
		if f.Key() != retryInField {
			continue
		}
		nanos, perr := strconv.ParseInt(f.StringValue(), 10, 64)
		if perr != nil {
			return 0, true
		}
		return time.Duration(max(nanos, 0)), true
	}
	return 0, true
}
