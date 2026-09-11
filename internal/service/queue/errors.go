// Package queue — declares the sentinel *errs.Error implementation outcomes.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package queue

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused consumer wiring is a
// permanent fault: the same call will be refused identically forever.
const exitConfig int = 78

// exitIOErr matches sysexits EX_IOERR (74). A filesystem that refused an
// operation may accept the next one, so the caller's retry is meaningful in a
// way a configuration refusal's is not.
const exitIOErr int = 74

var (
	// QueueBackendFailed wraps a refusal from the operating system: an open,
	// a rename, an unlink, a directory read or a flush.
	//
	// The cause stays in the chain, so a caller may still ask
	// errors.Is(err, fs.ErrNotExist) or reach the *fs.PathError. Its fields
	// name the operation and, where it is not the payload, the path — never
	// the message bytes.
	QueueBackendFailed = errs.Define(CodeQueueBackendFailed, "QUEUE_BACKEND_FAILED",
		"The queue's storage refused an operation",
		"service/queue: a filesystem call failed; the fields name the operation and the path, never the payload",
		errs.WithExitCode(exitIOErr))

	// QueueDirectoryUnusable refuses a queue directory at CONSTRUCTION.
	//
	// A durable queue whose directory any account may write is a queue any
	// account may inject work into or silently drain, and the failure is
	// invisible: messages simply stop arriving. It is the same refusal
	// service/lock makes about its lock directory, and it is made at wiring
	// time for the same reason — at first use it would be a mystery, at
	// construction it names the directory.
	QueueDirectoryUnusable = errs.Define(CodeQueueDirectoryUnusable, "QUEUE_DIRECTORY_UNUSABLE",
		"The queue directory cannot be used safely",
		"service/queue: the queue directory is missing, is not a directory, or is writable by other accounts",
		errs.WithExitCode(exitConfig))

	// ConsumerMisconfigured refuses a ConsumerConfig the engine cannot run.
	//
	// It is the sentinel behind the domain's one mandatory assertion. This
	// queue delivers AT LEAST ONCE, so a handler will be called twice for
	// one message — after a crash, after a lease expiry, after a slow
	// downstream. The SDK cannot detect whether that is safe, so it makes
	// the caller say so in code: ConsumerConfig.HandlerIsIdempotent, whose
	// zero value is false and is refused. That is exactly
	// resilience.HedgeConfig.Idempotent's instrument, for exactly its reason
	// — a precondition the SDK cannot check becomes a required, greppable
	// assertion instead of a comment nobody reads.
	ConsumerMisconfigured = errs.Define(CodeConsumerMisconfigured, "CONSUMER_MISCONFIGURED",
		"The consumer configuration is not runnable and was refused",
		"service/queue: Handler is nil, or HandlerIsIdempotent is false under an at-least-once queue; the field names which",
		errs.WithExitCode(exitConfig))

	// HandlerPanicked is the failure of a handler whose call panicked.
	//
	// The engine recovers it rather than letting it reach the runtime: a
	// panic escaping one handler would kill the consumer PROCESS, abandoning
	// every other in-flight lease — which then has to time out, one
	// visibility timeout at a time, before anything moves again. Recovered,
	// it becomes an ordinary nack: this message is retried and eventually
	// dead-lettered, and its siblings are untouched.
	//
	// The recovered value and the panicking goroutine's stack travel as
	// FIELDS, never as the wrap origin, so a panic carrying an *errs.Error
	// cannot hijack this code.
	// A handler that merely RETURNS an error gets no sentinel of its own:
	// Consume hands that error to Broker.Nack unmodified. service/events
	// joins its ListenerFailed verdict beside the listener's cause because
	// Publish returns the aggregate to a caller who must be able to ask "did
	// anything fail?"; here the aggregate's destination is a dead-letter
	// record, which already says that by existing and has structured fields
	// for the rest. A verdict around the cause would displace the reason an
	// investigator needs, and TestAFailingHandlerIsRetriedAndThenDeadLettered
	// pins that it does not.
	HandlerPanicked = errs.Define(CodeHandlerPanicked, "HANDLER_PANICKED",
		"The queue handler panicked and was recovered",
		"service/queue: handler panicked; the fields carry the message id, the panic value and the stack")
)
