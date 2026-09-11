// Package queue — the durable queue's state machine is a NAME. This file is
// its grammar: what a queued, an in-flight and an abandoned message are
// called, and how a name is read back into the facts it carries.
package queue

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// The three directories are the three states. A message is queued, leased, or
// abandoned, and it is in exactly one of them because rename(2) moves it
// between them indivisibly.
const (
	dirReady    string = "ready"
	dirInflight string = "inflight"
	dirDead     string = "dead"
)

// The two suffixes distinguish a message from anything else that may appear
// in one of those directories — notably the `.vfs-*.tmp` temporary an atomic
// publication creates beside its target, which a scan MUST skip rather than
// deliver as a half-written message.
const (
	suffixMessage string = ".msg"
	suffixDead    string = ".dead"
)

// nanoWidth zero-pads an instant to the width of the largest int64
// (9223372036854775807 is 19 digits), so that a LEXICOGRAPHIC sort of the
// directory is a CHRONOLOGICAL sort of its messages. That equality is the
// whole reason FIFO costs nothing here: os.ReadDir already sorts by name.
const nanoWidth int = 19

// deliveryWidth pads the delivery count. It is cosmetic — nothing sorts on it
// and it is parsed with strconv — but a fixed width keeps a directory listing
// readable by whoever is investigating at three in the morning.
const deliveryWidth int = 3

// decimalBase is the base every number in a name is written in. Decimal, not
// something denser, because the whole point of putting the state in the name
// is that a person can read it with ls.
const decimalBase int = 10

// idEntropyBytes is the entropy in a message identifier. Two messages
// published in the same nanosecond by two different PROCESSES have no
// sequence number to fall back on — that is what makes this queue
// inter-process — so the tie is broken by eight bytes of randomness rather
// than by a counter no other process can see.
const idEntropyBytes int = 8

// leaseEntropyBytes is the entropy in a receipt. It makes two consumers that
// lease two different messages in the same nanosecond unable to collide on an
// in-flight name, which matters because a rename onto an existing name would
// silently DESTROY the other consumer's message.
const leaseEntropyBytes int = 4

// int64Bits is the width strconv parses an instant at. It is named because a
// bare 64 in a ParseInt call is the one place a silent truncation would be
// invisible: an instant parsed at 32 bits would wrap in 2038.
const int64Bits int = 64

// The number of dot-separated fields each name carries.
const (
	readyFields    int = 4
	inflightFields int = 5
	deadFields     int = 3
)

// The position of each field within a queued or in-flight name. They are
// named rather than written as indices because the two layouts differ only by
// their last field, and an off-by-one between them would read a delivery
// count as a lease and be caught by nothing but a failing round trip.
const (
	posAt int = iota
	posEnqueued
	posEntropy
	posDeliveries
	posLease
)

// The position of each field within a dead letter's name, which carries no
// visibility instant and no lease.
const (
	posDeadEnqueued int = iota
	posDeadEntropy
	posDeadDeliveries
)

// stateDirs is the three state directories, hoisted so preparing a queue
// directory does not allocate a slice literal every time.
var stateDirs = []string{dirReady, dirInflight, dirDead}

// nameValue is a message's identity and state, as encoded in its filename.
//
// # Why the state is in the NAME and not in a file
//
// Because rename(2) is the only operation a POSIX filesystem offers that
// changes a fact indivisibly and refuses for the loser. Every transition this
// broker makes — queued to leased, leased back to queued, leased to abandoned
// — is one rename, so it either happened or it did not, no two consumers can
// both win it, and a process killed between two of them leaves the filesystem
// in a state the next process can read without recovering anything.
//
// The alternative — a state file, or a header rewritten in place — needs a
// lock to be read-modify-written, and a lock needs an owner that can die. The
// whole point of this broker is that its owner can die.
//
// # Why the instants are int64 nanoseconds
//
// Because that is what a NAME carries. A time.Time is a wall clock, a
// monotonic reading and a location, none of which survives being written into
// a filename — and holding two of them made this value large enough that
// every transition passed it by reference-sized copies it did not need. As
// nanoseconds the value fits comfortably below the linter's by-value
// threshold, and the conversion to a time.Time happens once, at the boundary
// where a caller actually wants one.
type nameValue struct {
	// Entropy is the message's random half; EnqueuedAt and it together are
	// the message identifier.
	Entropy string
	// Lease is the receipt's random half, empty for a queued message.
	Lease string
	// At is the instant the name sorts on, in Unix nanoseconds: the
	// visibility instant for a queued message, the lease deadline for an
	// in-flight one.
	At int64
	// EnqueuedAt is when the message was first accepted, in Unix
	// nanoseconds. It never changes.
	EnqueuedAt int64
	// Deliveries is how many times the message has been handed out.
	Deliveries int
}

// ID returns the opaque message identifier. It is stable across every
// redelivery and it is what a dead letter is joined to a log line by.
func (n nameValue) ID() string {
	//: the two halves that are fixed for the life of the message.
	return pad(n.EnqueuedAt) + "-" + n.Entropy
}

// AtTime returns [nameValue.At] as an instant, for the one boundary that
// wants one: the lease deadline handed to a consumer.
func (n nameValue) AtTime() time.Time {
	//: the conversion happens here and nowhere else.
	return time.Unix(0, n.At)
}

// EnqueuedTime returns [nameValue.EnqueuedAt] as an instant.
func (n nameValue) EnqueuedTime() time.Time {
	//: same boundary, other field.
	return time.Unix(0, n.EnqueuedAt)
}

// readyName renders a queued message's filename.
func (n nameValue) readyName() string {
	//: visibility first, so the sort order IS the delivery order.
	return strings.Join([]string{
		pad(n.At), pad(n.EnqueuedAt), n.Entropy, padCount(n.Deliveries),
	}, ".") + suffixMessage
}

// inflightName renders a leased message's filename, which is also its
// receipt.
func (n nameValue) inflightName() string {
	//: deadline first, so a reclaim scan meets the most overdue lease first.
	return strings.Join([]string{
		pad(n.At), pad(n.EnqueuedAt), n.Entropy, padCount(n.Deliveries), n.Lease,
	}, ".") + suffixMessage
}

// deadName renders an abandoned message's filename.
func (n nameValue) deadName() string {
	//: no visibility and no lease: a dead letter is never delivered again, so
	//: it sorts by the only instant that still means anything.
	return strings.Join([]string{
		pad(n.EnqueuedAt), n.Entropy, padCount(n.Deliveries),
	}, ".") + suffixDead
}

// parseReady reads a queued message's filename.
func parseReady(base string) (name nameValue, ok bool) {
	fields, split := splitName(base, suffixMessage, readyFields)
	//: anything that is not exactly this shape is not one of our messages —
	//: an atomic publication's temporary, an editor's backup, a stray file.
	if !split {
		//: the caller skips the entry.
		return nameValue{}, false
	}
	//: field 0 is the visibility instant for a queued message.
	return assemble(fields[posAt], fields[posEnqueued], fields[posEntropy],
		fields[posDeliveries], "")
}

// parseInflight reads a leased message's filename, which is what a receipt
// is.
//
// It is also the RECEIPT VALIDATOR, and that is why it is strict about the
// shape rather than merely splitting on dots: a receipt reaches this broker
// from a caller, and a caller that could put a separator or a ".." into one
// would be choosing which file Ack unlinks. The grammar admits only digits
// and lower-case hex, so no such receipt exists — and os.Root would refuse it
// anyway, which is the second of the two locks on that door.
func parseInflight(base string) (name nameValue, ok bool) {
	fields, split := splitName(base, suffixMessage, inflightFields)
	//: not a receipt this broker could have minted.
	if !split {
		//: the caller reports UnknownReceipt, or skips the entry.
		return nameValue{}, false
	}
	//: field 0 is the lease deadline for an in-flight message.
	return assemble(fields[posAt], fields[posEnqueued], fields[posEntropy],
		fields[posDeliveries], fields[posLease])
}

// parseDead reads an abandoned message's filename.
func parseDead(base string) (name nameValue, ok bool) {
	fields, split := splitName(base, suffixDead, deadFields)
	//: not a dead letter.
	if !split {
		//: the caller skips the entry.
		return nameValue{}, false
	}
	//: a dead letter has no visibility instant; At mirrors EnqueuedAt so the
	//: value stays total rather than carrying a zero nobody may read.
	return assemble(fields[posDeadEnqueued], fields[posDeadEnqueued], fields[posDeadEntropy],
		fields[posDeadDeliveries], "")
}

// assemble turns the parsed fields into a nameValue, refusing anything that
// is not a number where a number belongs or hex where hex belongs.
func assemble(at, enqueued, entropy, deliveries, lease string) (name nameValue, ok bool) {
	atNanos, atOK := parseNano(at)
	enqueuedNanos, enqueuedOK := parseNano(enqueued)
	count, countErr := strconv.Atoi(deliveries)
	//: every field is checked, because this function also validates the
	//: receipts a caller hands back.
	if !atOK || !enqueuedOK || countErr != nil || count < 0 || !isHex(entropy) || !isHex(lease) {
		//: refused.
		return nameValue{}, false
	}
	//: a well-formed name.
	return nameValue{
		At: atNanos, EnqueuedAt: enqueuedNanos,
		Entropy: entropy, Deliveries: count, Lease: lease,
	}, true
}

// splitName strips the suffix and cuts the base into exactly want fields.
func splitName(base, suffix string, want int) (fields []string, ok bool) {
	stem, trimmed := strings.CutSuffix(base, suffix)
	//: the suffix is what tells a message from a temporary.
	if !trimmed {
		//: not ours.
		return nil, false
	}
	cut := strings.Split(stem, ".")
	//: exactly — a name with one field too many is not a name we wrote.
	if len(cut) != want {
		//: not ours.
		return nil, false
	}
	//: the fields, in the order the renderers wrote them.
	return cut, true
}

// parseNano reads a zero-padded instant, refusing anything that is not
// exactly nanoWidth digits.
func parseNano(field string) (nanos int64, ok bool) {
	//: a shorter field would sort wrong; a longer one was not written here.
	if len(field) != nanoWidth {
		//: refused.
		return 0, false
	}
	value, err := strconv.ParseInt(field, decimalBase, int64Bits)
	//: ParseInt also refuses a sign, which the padding never emits.
	if err != nil || value < 0 {
		//: refused.
		return 0, false
	}
	//: a usable instant.
	return value, true
}

// isHex reports whether field is lower-case hexadecimal, the empty string
// included — a queued message has no lease half.
func isHex(field string) bool {
	//: every byte, because this also validates a caller's receipt.
	for index := range len(field) {
		char := field[index]
		//: the alphabet encoding/hex emits, and nothing else: no separator,
		//: no dot, no upper case, so no receipt can name another directory.
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			//: refused.
			return false
		}
	}
	//: acceptable.
	return true
}

// nameable reports whether at can be written into a name and read back.
//
// An instant in a name is nanoWidth digits of a NON-NEGATIVE int64, so the
// grammar covers the Unix epoch to 2262-04-11T23:47:16.854775807Z and nothing
// else. Outside that range UnixNano wraps, pad writes a sign, and parseNano
// refuses the name on the way back in — so a message renamed to such a name
// is stranded: never reclaimed, never delivered, its receipt unreadable.
func nameable(at time.Time) bool {
	//: both ends, because either end of int64 nanoseconds is a sign away.
	return !at.Before(time.Unix(0, 0)) && !at.After(time.Unix(0, math.MaxInt64))
}

// pad renders an instant as nanoWidth zero-padded digits.
func pad(nanos int64) string {
	digits := strconv.FormatInt(nanos, decimalBase)
	//: an instant before the epoch has a sign and would sort before
	//: everything; the broker's own clock never produces one, and a name that
	//: somehow carried one is refused on the way back in by parseNano.
	return strings.Repeat("0", max(0, nanoWidth-len(digits))) + digits
}

// padCount renders a delivery count as deliveryWidth zero-padded digits, or
// wider when the count needs it.
func padCount(count int) string {
	digits := strconv.Itoa(count)
	//: nothing sorts on this field, so overflowing the width is harmless.
	return strings.Repeat("0", max(0, deliveryWidth-len(digits))) + digits
}
