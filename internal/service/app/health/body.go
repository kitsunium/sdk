// Package health — hosts the wire shape of a probe response: the body, and the
// one check inside it. The two live together because the pair IS the document,
// and because keeping them in one file keeps the list of what may leave this
// process on one screen.
package health

// bodyValue is the wire shape of a probe response.
type bodyValue struct {
	// Status is the aggregate, in Status.String()'s stable spelling.
	Status string `json:"status"`
	// Checks is present only when HandlerConfig.Detail is set.
	Checks []checkValue `json:"checks,omitempty"`
}

// checkValue is the wire shape of one check inside a detailed body. Every
// field here is either the caller's own vocabulary (the name), a closed SDK
// enum, a duration, or an errs Public. Nothing else may be added: the raw
// error, the Private half and the fields are the three things this type exists
// to keep out.
type checkValue struct {
	// Name is the check's registered name, chosen by the caller.
	Name string `json:"name"`
	// Status is this check's verdict.
	Status string `json:"status"`
	// Reason is an errs Public, or the SDK's generic string. Absent when the
	// check passed.
	Reason string `json:"reason,omitempty"`
	// TookMs is how long the measurement took.
	TookMs int64 `json:"tookMs"`
	// AgeMs is how stale a replayed answer is. Absent when it was measured
	// during this probe.
	AgeMs int64 `json:"ageMs,omitempty"`
	// TimedOut distinguishes "the dependency said no" from "the dependency
	// said nothing". Absent when false.
	TimedOut bool `json:"timedOut,omitempty"`
	// Cached says the answer was replayed rather than measured. Absent when
	// false.
	Cached bool `json:"cached,omitempty"`
}
