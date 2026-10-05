package queue

import "github.com/kitsunium/sdk/internal/kernel/errs"

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
