// Package queue implements the asynchronous, durable message queue declared
// in internal/core/queue (ADR 0054): two brokers — one in the heap, one on the
// filesystem — the consumer engine that drives a [corequeue.Handler] against
// either, and the dead-letter record they both write.
//
// # Two brokers, one set of refusals
//
// [NewMemory] exists so a consumer can test its own code without a directory,
// and that is only worth something if the two answer identically. They
// therefore share the core policy guard (corequeue.PolicyValue.Validate), the
// same typed sentinels, and a table-driven conformance suite that runs the
// SAME cases against both. Where they cannot be identical — surviving a
// restart, crossing a process boundary, what a flush costs — the difference
// is named here rather than discovered by a reader.
//
// # The one thing the file broker does that the memory broker cannot
//
// It survives the consumer. Every piece of state the durable queue keeps —
// which messages are queued, which are leased, when each lease lapses, how
// many times each has been delivered — lives in a DIRECTORY ENTRY, not in
// this process's heap. A consumer that is SIGKILLed mid-handler leaves an
// entry whose deadline is in the past, and the next consumer to look, in any
// process on that machine, renames it back and delivers it again.
// TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack does exactly that,
// with a real subprocess and a real SIGKILL, because a test that calls Nack
// proves only that Nack works.
//
// # Why the durable broker holds no long-lived handle
//
// There is no Close in this package and no descriptor kept between calls.
// Every operation opens what it needs, moves what it must, and returns. That
// costs a syscall or two per call and buys the property the domain is for: a
// process that dies leaves nothing half-open, nothing to recover, and no
// state that only it could have interpreted. It is also what makes the
// broker genuinely inter-process — two brokers in two processes over one
// directory are the same queue, with no coordination beyond the filesystem.
//
// # Why there is no lock file
//
// Exclusion between consumers is [os.Rename], not a lock. Renaming a queued
// message into the in-flight directory is atomic and it FAILS for the loser,
// so two consumers reaching for the same message resolve it in the kernel
// with no lock, no lease and no polling — and it resolves the same way for
// two goroutines in one process as for two processes, which ADR 0052
// measured that flock(2) emphatically does not.
package queue

import (
	"crypto/rand"
	"encoding/hex"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// reasonLeaseExpired is the Reason recorded on a dead letter that died by
// running out of attempts without any consumer ever reporting a cause — the
// signature of a handler that crashes the process, since a process that dies
// cannot nack. It is spelled the same as core/queue.LeaseExpired's Reason on
// purpose: it is the same event, seen from the broker rather than from the
// consumer that lost the race.
const reasonLeaseExpired string = "LEASE_EXPIRED"

// reasonUnreported is the Reason recorded when a consumer nacked with a nil
// cause. "It did not work and I cannot say why" is a real answer and is
// recorded as one, rather than as an empty string a reader would take for a
// bug in this package.
const reasonUnreported string = "UNREPORTED"

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
