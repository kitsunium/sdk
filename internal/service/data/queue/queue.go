package queue

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"strings"
	"unicode/utf8"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// reasonLeaseExpired is the Reason recorded on a dead letter that died by
// running out of attempts without any consumer ever reporting a cause — the
// signature of a handler that crashes the process, since a process that dies
// cannot nack. It is spelled the same as core/data/queue.LeaseExpired's Reason on
// purpose: it is the same event, seen from the broker rather than from the
// consumer that lost the race.
const reasonLeaseExpired string = "LEASE_EXPIRED"

// reasonUnreported is the Reason recorded when a consumer nacked with a nil
// cause. "It did not work and I cannot say why" is a real answer and is
// recorded as one, rather than as an empty string a reader would take for a
// bug in this package.
const reasonUnreported string = "UNREPORTED"

// maxDeadDeliveries is the largest delivery count PutDeadLetter and
// PutMessage write: the
// SQL broker's deliveries column is a 32-bit INT on MySQL, and a count the
// table cannot hold is refused before a statement is sent, by every broker
// alike.
const maxDeadDeliveries int = math.MaxInt32

// causeValue is what a dead letter keeps of the failure that produced it:
// the SCREAMING_SNAKE reason, the wire-safe Public half, and the dotted-quad
// code.
//
// It is deliberately NOT the error. An error cannot be written to a device
// and read back as itself, and the half of it that could be — the Private
// message — is the half CLAUDE.md rule 4 defines as log-only. So the record
// keeps what a stranger investigating a queue is allowed to see, and the
// message identifier joins it to the log line the failing process wrote.
type causeValue struct {
	reason string
	public string
	code   uint32
}

// describeCause reduces an error to what a dead letter may keep of it.
func describeCause(cause error) causeValue {
	//: a consumer that knows only "this did not work" must still be able to
	//: say so, so a nil cause is recorded rather than refused.
	if cause == nil {
		//: named, so the absence is legible as a decision.
		return causeValue{reason: reasonUnreported}
	}
	described := causeValue{public: kerrs.PublicOf(cause)}
	reason, named := kerrs.ReasonOf(cause)
	//: a typed SDK error carries both halves; a stdlib error carries neither.
	if named {
		described.reason = reason
	}
	code, coded := kerrs.CodeOf(cause)
	//: 0 is "the cause carried no code", which is a fact and not a failure.
	if coded {
		described.code = uint32(code)
	}
	//: whatever the cause was willing to say in public.
	return described
}

// checkBatch refuses a non-positive batch size.
func checkBatch(max int) error {
	//: an empty slice forever is a consumer loop that spins and processes
	//: nothing, and looks exactly like an idle queue (ADR 0031).
	if max <= 0 {
		//: InvalidBatchSize.
		return kerrs.Wrap(corequeue.InvalidBatchSize, kerrs.WrapParams{}, kerrs.Int("max", max))
	}
	//: usable.
	return nil
}

// randomHex returns n bytes of entropy as hex.
//
// Hex rather than anything denser because the result has to be safe in a
// FILENAME and in a receipt alike, and because the name grammar's validator
// admits exactly this alphabet — which is what stops a receipt from ever
// naming a file outside the in-flight directory.
func randomHex(n int) (encoded string, err error) {
	raw := make([]byte, n)
	_, randErr := rand.Read(raw)
	//: an entropy source that fails is a real failure, not a thing to work
	//: around with a counter.
	if randErr != nil {
		//: the caller decides which sentinel this becomes.
		return "", randErr
	}
	//: usable in a name.
	return hex.EncodeToString(raw), nil
}

// checkSize refuses a payload larger than the policy allows.
//
// Its fields carry the two SIZES and never the payload: an error message that
// quoted the bytes would put a caller's data into every log that renders it,
// which is the classic way a queue leaks what it was carrying.
func checkSize(size, maxBytes int) error {
	//: the bound is enforced at the producer, the only place the payload can
	//: still be made smaller.
	if size > maxBytes {
		//: MessageTooLarge, with both numbers and no bytes.
		return kerrs.Wrap(corequeue.MessageTooLarge, kerrs.WrapParams{},
			kerrs.Int("size", size), kerrs.Int("max", maxBytes))
	}
	//: acceptable.
	return nil
}

// deadLetterNotFound is core/data/queue.DeadLetterNotFound for broker. The
// identifier travels as a log-only field: it is a name the broker minted, and
// never a byte of the payload.
func deadLetterNotFound(broker, id string) error {
	//: which broker, and which identifier it was asked for.
	return kerrs.Wrap(corequeue.DeadLetterNotFound, kerrs.WrapParams{},
		kerrs.String("broker", broker), kerrs.String("id", id))
}

// checkDeadLetter refuses a dead letter no broker keeps as it is given
// (core/data/queue.DeadLetterWriter), so the three brokers refuse identical
// inputs identically, then a payload the policy's bound refuses. What it
// reports names a field, never a value.
func checkDeadLetter(dead corequeue.DeadLetterValue, maxBytes int) error {
	field := ""
	switch {
	//: the identifier is the broker's to mint; one the caller set would not
	//: be kept, and a copy that silently renamed what it copied is refused.
	case dead.Message.ID != "":
		field = "Message.ID"
	//: a dead letter was delivered at least once, and a count a durable
	//: broker cannot record would come back as another number.
	case dead.Deliveries < 1 || dead.Deliveries > maxDeadDeliveries:
		field = "Deliveries"
	//: an instant outside int64 Unix nanoseconds — the zero time.Time
	//: included — is one no name and no column carries back.
	case !nameable(dead.Message.EnqueuedAt):
		field = "Message.EnqueuedAt"
	//: the same bound on the failure's instant.
	case !nameable(dead.FailedAt):
		field = "FailedAt"
	//: what every store would otherwise cut: one line, publicMaxRunes runes.
	case !keptAsWritten(dead.Reason):
		field = "Reason"
	//: the same bound on the cause.
	case !keptAsWritten(dead.Cause):
		field = "Cause"
	}
	//: one value no store keeps as given.
	if field != "" {
		//: InvalidDeadLetter, naming the field and never its value.
		return kerrs.Wrap(corequeue.InvalidDeadLetter, kerrs.WrapParams{}, kerrs.String("field", field))
	}
	//: MessageTooLarge, or nil: the bound Publish enforces.
	return checkSize(len(dead.Message.Payload), maxBytes)
}

// checkLiveMessage refuses a live message no broker keeps as it is given
// (core/data/queue.MessageWriter), so the three brokers refuse identical
// inputs identically, then a payload the policy's bound refuses. What it
// reports names a field, never a value.
func checkLiveMessage(live corequeue.LiveMessageValue, maxBytes int) error {
	field := ""
	switch {
	//: the identifier is the broker's to mint, as for a dead letter.
	case live.Message.ID != "":
		field = "Message.ID"
	//: a lease is the broker's that granted it; a written message waits.
	case live.Leased:
		field = "Leased"
	//: a count a durable broker cannot record would come back as another.
	case live.Deliveries < 0 || live.Deliveries > maxDeadDeliveries:
		field = "Deliveries"
	//: an instant outside int64 Unix nanoseconds is one no name and no
	//: column carries back.
	case !nameable(live.Message.EnqueuedAt):
		field = "Message.EnqueuedAt"
	//: the same bound on when it becomes visible.
	case !nameable(live.VisibleAt):
		field = "VisibleAt"
	}
	//: one value no broker keeps as given.
	if field != "" {
		//: InvalidMessage, naming the field and never its value.
		return kerrs.Wrap(corequeue.InvalidMessage, kerrs.WrapParams{}, kerrs.String("field", field))
	}
	//: MessageTooLarge, or nil: the bound Publish enforces.
	return checkSize(len(live.Message.Payload), maxBytes)
}

// keptAsWritten reports whether value survives oneLine unchanged: no line
// break, and at most publicMaxRunes runes.
func keptAsWritten(value string) bool {
	//: what oneLine would rewrite is refused instead of rewritten.
	return !strings.ContainsAny(value, "\r\n") && utf8.RuneCountInString(value) <= publicMaxRunes
}
