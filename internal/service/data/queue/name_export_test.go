// Package queue — hands the external fuzz targets the file broker's two
// hand-written readers of what it may not have written: the name grammar that
// validates a receipt, and the dead-letter record (issue #1047).
package queue

import (
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
)

// NameGrammarForTest is one state's grammar: its parser, and the renderer of
// what the parser read.
type NameGrammarForTest struct {
	// Parse reads a file name by the state's grammar.
	Parse func(base string) (rendered string, ok bool)
	// Suffix is the suffix every name of the state carries.
	Suffix string
}

// NameGrammarsForTest returns the grammars of ready/, inflight/ and dead/, by
// state directory.
func NameGrammarsForTest() map[string]NameGrammarForTest {
	render := func(parse func(string) (nameValue, bool), name func(nameValue) string) func(string) (string, bool) {
		return func(base string) (string, bool) {
			parsed, ok := parse(base)
			//: what the parser accepted, written back.
			if !ok {
				return "", false
			}
			return name(parsed), true
		}
	}
	//: the three grammars a state directory's names follow.
	return map[string]NameGrammarForTest{
		dirReady:    {Parse: render(parseReady, nameValue.readyName), Suffix: suffixMessage},
		dirInflight: {Parse: render(parseInflight, nameValue.inflightName), Suffix: suffixMessage},
		dirDead:     {Parse: render(parseDead, nameValue.deadName), Suffix: suffixDead},
	}
}

// ReadyNameForTest renders the ready/ name of a message, for a seed.
func ReadyNameForTest(at, enqueued int64, entropy string, deliveries int) string {
	//: the queued grammar.
	return nameValue{At: at, EnqueuedAt: enqueued, Entropy: entropy, Deliveries: deliveries}.readyName()
}

// InflightNameForTest renders the inflight/ name of a message, for a seed.
func InflightNameForTest(at, enqueued int64, entropy string, deliveries int, lease string) string {
	//: the leased grammar.
	return nameValue{At: at, EnqueuedAt: enqueued, Entropy: entropy, Deliveries: deliveries, Lease: lease}.inflightName()
}

// DeadNameForTest renders the dead/ name of a message, for a seed.
func DeadNameForTest(enqueued int64, entropy string, deliveries int) string {
	//: the dead grammar.
	return nameValue{EnqueuedAt: enqueued, Entropy: entropy, Deliveries: deliveries}.deadName()
}

// DecodeDeadForTest is decodeDead.
func DecodeDeadForTest(raw []byte) (corequeue.DeadLetterValue, bool) {
	//: the record reader the file broker runs on every file of dead/.
	return decodeDead(raw)
}

// EncodeDeadForTest writes record as a burial writes it, and reports false
// when record cannot be written as it reads: an identifier the broker does
// not mint, an enqueue instant it does not carry, or a zero instant.
func EncodeDeadForTest(record corequeue.DeadLetterValue) ([]byte, bool) {
	enqueued, entropy, minted := parseMessageID(record.Message.ID)
	//: a record whose header disagrees with itself has no single encoding.
	if !minted || record.FailedAt.IsZero() || record.Message.EnqueuedAt.IsZero() ||
		enqueued != record.Message.EnqueuedAt.UnixNano() {
		return nil, false
	}
	name := nameValue{EnqueuedAt: enqueued, Entropy: entropy, Deliveries: record.Deliveries}
	cause := causeValue{reason: record.Reason, public: record.Cause, code: record.Code, trail: record.Trail}
	//: the burial's own writer.
	return encodeDead(name, cause, record.FailedAt.UnixNano(), record.Message.Payload), true
}

// EncodeTrailForTest is encodeTrail, and DecodeTrailForTest decodeTrail.
func EncodeTrailForTest(trail []uint32) string {
	//: the text form both durable brokers write.
	return encodeTrail(trail)
}

// DecodeTrailForTest is decodeTrail.
func DecodeTrailForTest(text string) []uint32 {
	//: the text form read back.
	return decodeTrail(text)
}

// MaxDeadTrailForTest is the most codes a trail keeps.
const MaxDeadTrailForTest int = maxDeadTrail

// FailedAtForTest is t as a burial records it, for a seed.
func FailedAtForTest(t time.Time) int64 {
	//: Unix nanoseconds.
	return t.UnixNano()
}

// SetListingWallForTest replaces the real clock a file broker's listings are
// judged on (file_listing.go, issue #811), so a case can stand at a chosen
// distance from a listing without waiting for it.
func SetListingWallForTest(broker corequeue.Broker, wall func() time.Time) {
	//: the durable broker's own field; any other broker is a test's mistake.
	broker.(*fileBroker).listed.wall = wall
}

// RacyMarginForTest is how much older than its listing a directory's mtime
// must be for the listing to be reused, and ListingMaxAgeForTest how long a
// listing is reused at most.
const (
	RacyMarginForTest    time.Duration = racyMargin
	ListingMaxAgeForTest time.Duration = listingMaxAge
)

// WrapPublisherForTest replaces a file broker's atomic publisher with what
// wrap makes of it, so a case can count or fail the publications a call
// makes (issue #827).
func WrapPublisherForTest(broker corequeue.Broker, wrap func(corevfs.AtomicWriter) corevfs.AtomicWriter) {
	//: the durable broker's own field.
	fb := broker.(*fileBroker)
	fb.publisher = wrap(fb.publisher)
}
