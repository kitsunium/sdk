package queue

import (
	"slices"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// retryAfterField names the field RetryAfter carries its delay in, as integer
// nanoseconds. RetryAfterOf is its one reader, so the spelling is this
// package's alone.
const retryAfterField string = "retry_after_ns"

// delayBits is the width a delay is parsed back at: a time.Duration's.
const delayBits int = 64

// delayBase is the radix errs renders an integer field in.
const delayBase int = 10

// DoNotRetry marks cause as a failure no retry can fix, so the consumer engine
// dead-letters the message at once instead of handing it back for another
// attempt (ADR 0151).
//
//	var order Order
//	if err := json.Unmarshal(d.Message.Payload, &order); err != nil {
//		return queue.DoNotRetry(err) // the same bytes will never decode
//	}
//
// It is for a failure that belongs to the MESSAGE — a payload that does not
// decode, a reference to something since deleted, a request the handler will
// refuse however often it is asked — and never to the moment: a downstream
// that is down, a lock that is held, a deadline that passed are what retries
// are for. Retrying the first kind spends [PolicyValue.MaxDeliveries] leases
// and retry delays on an outcome already known, and keeps the dead letter an
// operator needs to see out of the store for as long.
//
// The mark is an errs.Wrap onto cause, so what a dead letter records of the
// failure is unchanged: an SDK error keeps its own Reason, Code and Public —
// origin wins, and [CodeNotRetryable] joins only its wrap trail — while a
// cause that is not an SDK error, or no cause at all, records [NotRetryable]
// itself, whose Public says what happened. Recognise the mark with
// errs.HasCode(err, CodeNotRetryable); errors.Is(err, NotRetryable) answers
// only where NotRetryable is the origin.
//
// The engine reaches the broker's [Rejecter] for it. A broker without that
// sibling is nacked as before, so the message is retried until
// MaxDeliveries and dead-lettered with the same cause: the shortcut is lost,
// never the message. A panic is never read as the mark — the recovered value
// travels as a field of the engine's own verdict, not as its origin.
func DoNotRetry(cause error) error {
	//: ONE call for both paths, by errs.Wrap's own rule: an SDK cause stays
	//: the origin and takes only the Code, onto its wrap trail; anything else
	//: — a foreign error, or nil — takes all of these as its origin. The
	//: Reason and words below are therefore what a dead letter records for a
	//: cause that has none of its own, and never replace one that has.
	return errs.Wrap(cause, errs.WrapParams{
		Code: CodeNotRetryable, Reason: "NOT_RETRYABLE",
		Public:   "The message cannot be processed and was dead-lettered without a retry",
		Private:  "core/data/queue: a handler declared a failure no retry can fix; the engine dead-letters the message at once with the handler's own cause",
		ExitCode: exitDataErr,
	})
}

// RetryAfter marks cause as a failure to retry no sooner than delay from now,
// so the consumer engine hands the message back through the broker's
// [DelayedNacker] with that delay instead of the policy's retry delay
// (ADR 0183).
//
//	if resp.StatusCode == http.StatusTooManyRequests {
//		return queue.RetryAfter(errThrottled, retryAfter(resp)) // what the server asked for
//	}
//
// It is for a failure whose moment is known — a downstream that answered
// with a Retry-After, a mail server that greylisted the sender, a resource
// that frees up at a known instant. The delivery is counted as a nack counts
// it, so a message that has used its [PolicyValue.MaxDeliveries] is
// dead-lettered with cause, the delay unused. A negative delay is read as
// zero — retry at once — and one above [MaxDeadlineOffset] is held there.
//
// The mark is an errs.Wrap onto cause, as [DoNotRetry]'s is: an SDK cause
// keeps its own Reason, Code and Public, [CodeRetryRequested] joins its wrap
// trail and the delay rides as a field; any other cause, or none, records
// [RetryRequested] itself. [RetryAfterOf] reads the delay back. A failure
// marked both ways is not retried: [DoNotRetry] wins.
//
// A broker without the sibling is nacked as before: the message comes back
// after the policy's retry delay, so the delay asked for is lost, never the
// message.
func RetryAfter(cause error, delay time.Duration) error {
	//: one reading for a negative delay — now — and a ceiling past which no
	//: durable broker could record the instant.
	held := min(max(delay, 0), MaxDeadlineOffset)
	//: ONE call for both paths, as DoNotRetry's: an SDK cause stays the origin
	//: and takes only the Code onto its wrap trail, anything else takes all of
	//: these as its origin. The delay rides as a field either way.
	return errs.Wrap(cause, errs.WrapParams{
		Code: CodeRetryRequested, Reason: "RETRY_REQUESTED",
		Public:   "The message was handed back to be retried after a delay",
		Private:  "core/data/queue: a handler asked for a retry after a delay of its own; the delay rides as the retry_after_ns field",
		ExitCode: exitTempFail,
	}, errs.Int64(retryAfterField, int64(held)))
}

// RetryAfterOf returns the delay [RetryAfter] put on err, and whether it put
// one: err carries [CodeRetryRequested] in its chain and the delay it was
// marked with. When err was marked more than once, the outermost mark's
// delay is the one returned.
func RetryAfterOf(err error) (delay time.Duration, marked bool) {
	//: the code is the mark; a field of the same name without it is somebody
	//: else's.
	if !errs.HasCode(err, CodeRetryRequested) {
		//: not marked.
		return 0, false
	}
	//: every layer's fields, inner first, so the last match is the outermost
	//: mark's.
	for _, field := range slices.Backward(errs.FieldsOf(err)) {
		//: another field.
		if field.Key() != retryAfterField {
			continue
		}
		nanos, parseErr := strconv.ParseInt(field.StringValue(), delayBase, delayBits)
		//: a field of that name that is not RetryAfter's integer.
		if parseErr != nil {
			//: marked, with no delay that can be read: now.
			return 0, true
		}
		//: the delay asked for, held where RetryAfter holds it.
		return min(max(time.Duration(nanos), 0), MaxDeadlineOffset), true
	}
	//: marked, but the field did not travel — a mark joined beside another
	//: error rather than wrapping it. Now is the one reading left.
	return 0, true
}
