// Package queue — range 0.2.23.* (ADR 0054 core/data/queue block), and the
// brokers' and the consumer engine's 0.3.53.* (ADR 0054 service/data/queue
// block, declared here since ADR 0160).
//
// Package queue — the two ADR 0039 siblings that act on the dead-letter store:
// sending a message there at once, and deciding what becomes of one there.
//
// Package queue — declares the sentinel *errs.Error port outcomes, and the
// implementation outcomes of the brokers and the consumer engine in
// internal/service/data/queue (ADR 0160: every code is declared in the core,
// at the service's path). Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package queue

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

// exitIOErr matches sysexits EX_IOERR (74). A filesystem that refused an
// operation may accept the next one, so the caller's retry is meaningful in a
// way a configuration refusal's is not.
const exitIOErr int = 74

// httpNotFound is 404: the identifier names no dead letter.
const httpNotFound int = 404
