// Package events — declares the sentinel *errs.Error port outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package events

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Subscribe will be refused forever, and the
// fix is a code change at the call site, never a retry.
const exitConfig int = 78

var (
	// InvalidSubscription is returned by Subscribe for a subscription that
	// could never run. The "missing" field names which half is absent.
	InvalidSubscription = errs.Define(CodeInvalidSubscription, "INVALID_SUBSCRIPTION",
		"The event subscription is not runnable and was refused",
		"core/events: subscription has an empty name or a nil Listener; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateListener is returned by Subscribe when the name is already
	// taken for that event type. Names are how an error and an Unsubscribe
	// identify a listener, so two listeners sharing one would make every
	// report ambiguous and every removal a coin toss.
	DuplicateListener = errs.Define(CodeDuplicateListener, "DUPLICATE_LISTENER",
		"A listener is already registered under that name for this event",
		"core/events: listener names are unique per event type; the fields carry the name and the type",
		errs.WithExitCode(exitConfig))

	// UnknownListener is returned by Unsubscribe for a name that is not
	// registered. Reporting it beats returning nil: "removed" and "there was
	// nothing to remove" are different answers, and a caller who gets the
	// second while expecting the first has a bug that silence would hide
	// until the listener they meant to remove fired again.
	UnknownListener = errs.Define(CodeUnknownListener, "UNKNOWN_LISTENER",
		"No listener is registered under that name for this event",
		"core/events: Unsubscribe named a listener this bus does not hold; the fields carry the name and the type",
		errs.WithExitCode(exitConfig))

	// InvalidEventType is returned by Subscribe for a nil or interface event
	// type, and by Publish for a nil event.
	//
	// The interface case is the one worth refusing loudly. Publish resolves
	// an event's type with reflect.TypeOf, which yields the value's DYNAMIC
	// type and never an interface type, so a subscription keyed on an
	// interface would be registered, reported by every introspection, and
	// never called once. That is the exact shape ADR 0031 removes from this
	// SDK: a registration that looks installed and is inert.
	InvalidEventType = errs.Define(CodeInvalidEventType, "INVALID_EVENT_TYPE",
		"The event type is not a concrete type and could never be dispatched",
		"core/events: event types must be concrete; a nil or interface type would never match a published value",
		errs.WithExitCode(exitConfig))

	// ListenerPanicked is the failure of a listener whose call panicked. The
	// bus recovers it rather than letting it reach the runtime, because a
	// panic escaping a listener would kill the PUBLISHER — code that did
	// nothing wrong and, in the synchronous contract this domain promises, is
	// usually the caller's request or transaction. The recovered value and
	// the panicking goroutine's stack travel as fields; the value is never
	// the wrap origin, so a panic carrying an *errs.Error cannot hijack this
	// code.
	ListenerPanicked = errs.Define(CodeListenerPanicked, "LISTENER_PANICKED",
		"The event listener panicked and was recovered",
		"core/events: listener panicked; the fields carry its name, the event type, the value and the stack")

	// Halt is the control sentinel a listener returns to stop the dispatch:
	// the listeners after it in priority order are not called.
	//
	// It is an error VALUE rather than a second return parameter because the
	// error slot is already in the signature, and widening a published FUNC
	// port to (bool, error) would break every listener ever written against
	// it — ADR 0039's rule, applied to the shape rather than to a method set.
	// The stdlib's own io.EOF is the same instrument: a sentinel that means
	// "stop", matched with errors.Is, never rendered to a user.
	//
	// Only a subscription with MayHalt set may return it. From any other
	// listener it is a HALT_NOT_PERMITTED failure (internal/service/events) and the
	// dispatch continues.
	Halt = errs.Define(CodeHalt, "HALT",
		"A listener stopped the event dispatch",
		"core/events: control sentinel consumed by the bus and reported through DispatchValue; never returned to a publisher")
)
