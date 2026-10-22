package queue

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
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

// maxDeadTrail is the most trail codes a dead letter keeps: the kernel's own
// bound on an error's trail (errs keeps sixteen, its origin's first wrap and
// the newest), so a trail describeCause reads is never cut, and one
// PutDeadLetter is handed longer is refused rather than cut, by every broker
// alike.
const maxDeadTrail int = 16

// trailSeparator joins a trail's codes where a broker writes it as text: the
// file broker's header line and the SQL broker's column.
const trailSeparator string = ","

// causeValue is what a dead letter keeps of the failure that produced it:
// the SCREAMING_SNAKE reason, the wire-safe Public half, the dotted-quad
// code, and the codes its wraps added — the trail, where a sentinel a wrapper
// added stays readable once the origin's code is all the rest says.
//
// It is deliberately NOT the error. An error cannot be written to a device
// and read back as itself, and the half of it that could be — the Private
// message — is the half CLAUDE.md rule 4 defines as log-only. So the record
// keeps what a stranger investigating a queue is allowed to see, and the
// message identifier joins it to the log line the failing process wrote.
type causeValue struct {
	reason string
	public string
	trail  []uint32
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
	described.trail = trailCodes(kerrs.TrailOf(cause))
	//: whatever the cause was willing to say in public.
	return described
}

// trailCodes is what a dead letter keeps of an error's trail: its codes as
// uint32, oldest first, the newest maxDeadTrail when there are more, and nil
// for none — so a cause with no trail writes nothing a reader must skip.
func trailCodes(trail []kerrs.Code) []uint32 {
	//: no wrap added a code.
	if len(trail) == 0 {
		//: nothing to keep.
		return nil
	}
	kept := trail[max(len(trail)-maxDeadTrail, 0):]
	codes := make([]uint32, len(kept))
	//: each code as the value carries it.
	for i, code := range kept {
		codes[i] = uint32(code)
	}
	//: oldest first, as the error holds them.
	return codes
}

// cloneTrail is a copy of trail a caller cannot rewrite the store's through,
// nil for an empty one, as every broker reads an empty trail back.
func cloneTrail(trail []uint32) []uint32 {
	//: none kept.
	if len(trail) == 0 {
		//: nil, never an empty slice.
		return nil
	}
	//: the caller's own.
	return slices.Clone(trail)
}

// encodeTrail renders a trail as a broker writes it as text: its codes in
// decimal, oldest first, comma-separated; "" for none.
func encodeTrail(trail []uint32) string {
	parts := make([]string, len(trail))
	//: each code as the "code" field is written.
	for i, code := range trail {
		parts[i] = strconv.FormatUint(uint64(code), decimalBase)
	}
	//: "" for an empty trail.
	return strings.Join(parts, trailSeparator)
}

// decodeTrail reads back what encodeTrail wrote. A trail it cannot read
// whole — a code that is not a uint32, more than maxDeadTrail of them — is
// read as none: a corrupt field costs its own value and never the record.
func decodeTrail(text string) []uint32 {
	//: none written: a dead letter whose cause had no trail, or one buried
	//: before trails were kept.
	if text == "" {
		//: no trail.
		return nil
	}
	parts := strings.Split(text, trailSeparator)
	//: more than any broker writes.
	if len(parts) > maxDeadTrail {
		//: no trail.
		return nil
	}
	codes := make([]uint32, len(parts))
	//: every code, or none of them.
	for i, part := range parts {
		code, err := strconv.ParseUint(part, decimalBase, uint32Bits)
		//: not a code this package wrote.
		if err != nil {
			//: no trail.
			return nil
		}
		codes[i] = uint32(code)
	}
	//: the trail the burial kept.
	return codes
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
	field, refused := messageField(dead)
	//: the message is kept as given; its failure may not be.
	if !refused {
		field, refused = causeField(dead)
	}
	//: one value no store keeps as given.
	if refused {
		//: InvalidDeadLetter, naming the field and never its value.
		return kerrs.Wrap(corequeue.InvalidDeadLetter, kerrs.WrapParams{}, kerrs.String("field", field))
	}
	//: MessageTooLarge, or nil: the bound Publish enforces.
	return checkSize(len(dead.Message.Payload), maxBytes)
}

// messageField names the part of dead's message, its count or its instants
// that no store keeps as it is given, and reports whether there is one.
func messageField(dead corequeue.DeadLetterValue) (field string, refused bool) {
	switch {
	//: the identifier is the broker's to mint; one the caller set would not
	//: be kept, and a copy that silently renamed what it copied is refused.
	case dead.Message.ID != "":
		//: the identifier.
		return "Message.ID", true
	//: a dead letter was delivered at least once, and a count a durable
	//: broker cannot record would come back as another number.
	case dead.Deliveries < 1 || dead.Deliveries > maxDeadDeliveries:
		//: the count.
		return "Deliveries", true
	//: an instant outside int64 Unix nanoseconds — the zero time.Time
	//: included — is one no name and no column carries back.
	case !nameable(dead.Message.EnqueuedAt):
		//: the enqueue instant.
		return "Message.EnqueuedAt", true
	//: the same bound on the failure's instant.
	case !nameable(dead.FailedAt):
		//: the failure's instant.
		return "FailedAt", true
	default:
		//: kept as given.
		return "", false
	}
}

// causeField names the part of dead's failure no store keeps as it is
// given, and reports whether there is one.
func causeField(dead corequeue.DeadLetterValue) (field string, refused bool) {
	switch {
	//: what every store would otherwise cut: one line, publicMaxRunes runes.
	case !keptAsWritten(dead.Reason):
		//: the reason.
		return "Reason", true
	//: the same bound on the cause.
	case !keptAsWritten(dead.Cause):
		//: the cause.
		return "Cause", true
	//: the kernel's own bound on a trail; a longer one would be cut.
	case len(dead.Trail) > maxDeadTrail:
		//: the trail.
		return "Trail", true
	default:
		//: kept as given.
		return "", false
	}
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

// nackDeadline is the instant a message NackAfter hands back becomes
// receivable: delay after now, a negative delay read as zero
// (core/data/queue.DelayedNacker). An instant no durable broker records is
// refused, by every broker alike, before anything moves.
func nackDeadline(now time.Time, delay time.Duration) (time.Time, error) {
	visibleAt := now.Add(max(delay, 0))
	//: past 2262 a name or a column would carry it wrapped.
	if !nameable(visibleAt) {
		//: QueueMisconfigured, naming the argument and never a payload.
		return time.Time{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "NackAfter.delay"), kerrs.Int64("value_ns", int64(delay)))
	}
	//: the instant, on the broker's clock.
	return visibleAt, nil
}

// publishVisibleAt is the instant a message PublishAt writes becomes
// receivable: at, or now when at has already passed
// (core/data/queue.DelayedPublisher). An instant no durable broker records is
// refused, by every broker alike, before anything is written.
func publishVisibleAt(now, at time.Time) (time.Time, error) {
	//: a past instant — the zero time.Time included — has one reading: now.
	if !at.After(now) {
		//: receivable at once, as a publication is.
		return now, nil
	}
	//: past 2262 a name or a column would carry it wrapped.
	if !nameable(at) {
		//: QueueMisconfigured, naming the argument.
		return time.Time{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "PublishAt.at"), kerrs.String("instant", at.UTC().Format(time.RFC3339Nano)))
	}
	//: held back until then.
	return at, nil
}

// onceUntil is the instant a PublishOnce reservation made now ends
// (core/data/queue.OncePublisher), after the refusals every broker makes
// alike: an empty key, a window that is not positive — "never deduplicate"
// and "forever" are the opposite readings of zero — and a window whose end no
// durable broker records.
func onceUntil(now time.Time, key string, window time.Duration) (time.Time, error) {
	field := ""
	until := now.Add(window)
	switch {
	//: an empty key would make every keyless publication one message.
	case key == "":
		field = "PublishOnce.key"
	//: zero and below have two readings, and both are harmful.
	case window <= 0 || !nameable(until):
		field = "PublishOnce.window"
	}
	//: one argument no broker can honour.
	if field != "" {
		//: QueueMisconfigured, naming the argument — never the key, which may
		//: be derived from the payload.
		return time.Time{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", field), kerrs.Int64("window_ns", int64(window)))
	}
	//: the reservation's end, on the broker's clock.
	return until, nil
}

// onceDigest is what a durable broker keeps of a PublishOnce key: its SHA-256,
// in lower-case hex. Fixed-width whatever the key, so it is a file name and a
// column value with no escaping, and never the key itself, which a caller may
// derive from what the message carries.
func onceDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	//: 64 hex digits.
	return hex.EncodeToString(sum[:])
}

// keptAsWritten reports whether value survives oneLine unchanged: no line
// break, and at most publicMaxRunes runes.
func keptAsWritten(value string) bool {
	//: what oneLine would rewrite is refused instead of rewritten.
	return !strings.ContainsAny(value, "\r\n") && utf8.RuneCountInString(value) <= publicMaxRunes
}
