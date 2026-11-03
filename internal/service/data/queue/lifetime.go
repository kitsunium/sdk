package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// retentionSweepEvery is how often, at most, a broker's Receive looks for a
// dead letter past PolicyValue.DeadLetterRetention, on the broker's clock: a
// retention is days, so a minute late costs nothing, and a Receive that swept
// on every poll would pay a scan of the dead-letter store each time.
const retentionSweepEvery time.Duration = time.Minute

// expiredCause is what a dead letter records of a message past
// PolicyValue.MaxAge: MESSAGE_EXPIRED, its code and its public words.
func expiredCause() causeValue {
	//: the sentinel reduced as any cause is.
	return describeCause(corequeue.MessageExpired)
}

// messageExpired reports whether a message enqueued at enqueued — Unix
// nanoseconds — has reached policy's MaxAge by now: never when MaxAge is zero.
func messageExpired(policy corequeue.PolicyValue, enqueued, now int64) bool {
	//: zero keeps a message for good; Validate bounds MaxAge to a century, so
	//: the sum stays inside an int64 for any instant a name can carry.
	return policy.MaxAge > 0 && now-enqueued >= int64(policy.MaxAge)
}

// retentionDue reports whether a broker whose last retention sweep ran at
// swept — Unix nanoseconds, zero for never — sweeps again at now: when the
// policy keeps dead letters for a time at all, and a minute has passed.
func retentionDue(policy corequeue.PolicyValue, swept, now int64) bool {
	//: zero keeps every dead letter for good.
	if policy.DeadLetterRetention <= 0 {
		//: no sweep.
		return false
	}
	//: the first sweep, then at most one a minute.
	return swept == 0 || now-swept >= int64(retentionSweepEvery)
}

// retentionCutoff is the instant before which a dead letter has outlived
// policy's DeadLetterRetention at now: one that failed at or before it goes.
func retentionCutoff(policy corequeue.PolicyValue, now int64) int64 {
	//: Validate bounds the retention to a century.
	return now - int64(policy.DeadLetterRetention)
}
