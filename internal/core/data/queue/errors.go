// Package queue — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package queue

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused policy or a
// non-positive batch size is a permanent wiring fault: the same call will be
// refused identically forever, and the fix is a code change at the call site,
// never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A lapsed lease is not a
// wiring fault — the code was right and the clock was faster — and the
// message it names is back in the queue, so the work is not lost and the
// consumer's next Receive is the retry.
const exitTempFail int = 75

// exitDataErr matches sysexits EX_DATAERR (65). A failure a handler declares
// no retry can fix is almost always the message itself — a payload that does
// not decode, a reference to something since deleted — and the same bytes are
// refused the same way however often they are delivered.
const exitDataErr int = 65

// exitNoInput matches sysexits EX_NOINPUT (66): the dead letter a replay or a
// deletion names is not there to be read.
const exitNoInput int = 66

// httpNotFound is 404: the identifier names no dead letter.
const httpNotFound int = 404

var (
	// QueueMisconfigured refuses a PolicyValue a broker cannot honour — a
	// zero with opposite readings, or a duration past MaxDeadlineOffset — and
	// an Extend duration that is non-positive or reaches past what a durable
	// broker can record. The "field" field names which value.
	//
	// It is a refusal and not a clamp for the fields whose zero has two
	// opposite readings — see PolicyValue's documentation, which also names
	// the two fields this sentinel deliberately does NOT guard.
	QueueMisconfigured = errs.Define(CodeQueueMisconfigured, "QUEUE_MISCONFIGURED",
		"The queue policy is not one this broker can honour",
		"core/data/queue: a policy field has no usable value and no default the SDK could invent; the field names which",
		errs.WithExitCode(exitConfig))

	// MessageTooLarge refuses a Publish whose payload exceeds the policy's
	// bound. It is refused at the producer, because that is the only place
	// the payload can still be made smaller — a consumer discovering it
	// cannot do anything about it, and a broker accepting it lets one
	// producer fill a device every other producer shares.
	//
	// Its fields carry the two SIZES and never the payload.
	MessageTooLarge = errs.Define(CodeMessageTooLarge, "MESSAGE_TOO_LARGE",
		"The message payload exceeds the queue's configured maximum size",
		"core/data/queue: payload larger than PolicyValue.MaxMessageBytes; the fields carry both sizes, never the bytes",
		errs.WithExitCode(exitConfig))

	// UnknownReceipt refuses an acknowledgement naming a lease this broker
	// never issued, or one whose encoding it cannot read.
	//
	// It is distinct from LeaseExpired on purpose. "This was yours and you
	// were too slow" and "this was never yours" are different bugs: the
	// first is a visibility timeout too short for the handler, the second is
	// a receipt built by hand, kept across a restart of an in-memory broker,
	// or handed to the wrong queue. Collapsing them would send every reader
	// looking for the wrong one half the time.
	UnknownReceipt = errs.Define(CodeUnknownReceipt, "UNKNOWN_RECEIPT",
		"The receipt does not name a lease this queue issued",
		"core/data/queue: receipt unparseable or never issued by this broker; the field carries no payload",
		errs.WithExitCode(exitConfig))

	// LeaseExpired reports an Ack, Nack or Extend whose lease had already
	// lapsed. NOTHING happened: the message is back in the queue or in the
	// dead-letter store, and another consumer may already hold it.
	//
	// Reporting it beats returning nil, for the reason core/lock gives about
	// releasing a lock one no longer holds: a holder that lost its claim must
	// not be able to end somebody else's turn, and must find out that it lost
	// it. A consumer that gets this has produced a duplicate — which the
	// at-least-once guarantee permits — and the one thing it must not do is
	// believe the work is finished.
	LeaseExpired = errs.Define(CodeLeaseExpired, "LEASE_EXPIRED",
		"The lease on that message has expired and the acknowledgement was ignored",
		"core/data/queue: visibility timeout elapsed before Ack/Nack/Extend; the message is queued again or dead-lettered",
		errs.WithExitCode(exitTempFail))

	// InvalidBatchSize refuses a Receive or DeadLetters asked for a
	// non-positive count.
	//
	// Returning an empty slice instead would be ADR 0031's inert policy: a
	// consumer loop asking for zero messages spins forever, processes
	// nothing, reports no error, and looks exactly like an idle queue.
	InvalidBatchSize = errs.Define(CodeInvalidBatchSize, "INVALID_BATCH_SIZE",
		"A batch size must be positive",
		"core/data/queue: Receive/DeadLetters called with max <= 0, which would poll forever and deliver nothing",
		errs.WithExitCode(exitConfig))

	// NotRetryable is the failure a handler reports when no retry can fix it:
	// returned as it is, or — the usual form — added by DoNotRetry to the
	// handler's own cause. The consumer engine recognises its code with
	// errs.HasCode and dead-letters the message at once through a broker's
	// Rejecter, instead of spending MaxDeliveries leases on an outcome
	// already known.
	//
	// Returned bare, it is the cause the dead letter records. Added by
	// DoNotRetry to an SDK error it is only a mark in the wrap trail, and the
	// error's own Reason, Code and Public are what the dead letter keeps:
	// origin wins (CLAUDE.md rule 6), which is the reason an investigator
	// needs.
	NotRetryable = errs.Define(CodeNotRetryable, "NOT_RETRYABLE",
		"The message cannot be processed and was dead-lettered without a retry",
		"core/data/queue: a handler declared a failure no retry can fix; the engine dead-letters the message at once with the handler's own cause",
		errs.WithExitCode(exitDataErr))

	// DeadLetterNotFound refuses a replay or a deletion naming a dead letter
	// the store does not hold: one that never died, or was already replayed
	// or deleted — by this caller or, concurrently, by another. Nothing moved.
	DeadLetterNotFound = errs.Define(CodeDeadLetterNotFound, "DEAD_LETTER_NOT_FOUND",
		"No dead letter has that identifier",
		"core/data/queue: the dead-letter store holds no message under the identifier; nothing was replayed or deleted",
		errs.WithExitCode(exitNoInput), errs.WithHTTPStatus(httpNotFound))
)
