// Package session — the description a Store fills in to build a SessionValue.
package session

import "time"

// StateValue is the full description of a session, with exported fields, from
// which [NewSessionValue] builds an immutable [SessionValue]. It exists because
// [Store] is a PUBLISHED port: a framework writing its own store — Redis,
// Postgres, whatever the deployment already runs — has to be able to construct
// the value type it is contracted to return, and every field of [SessionValue]
// is unexported.
//
// It is not a back door around the fixation rule. A caller can certainly build
// a StateValue naming any subject; what they cannot do is persist it, because
// [Store.Save] compares the subject against the stored record and refuses a
// mismatch with [FixationRefused]. The forgery is possible and useless, which
// is the property worth having: the refusal is a runtime verdict a test can
// reach, not a comment asking people to behave.
type StateValue struct {
	// ID names the session. NewSessionValue refuses a zero one.
	ID ID
	// Subject is the authenticated principal, or "" for anonymous.
	Subject string
	// CreatedAt anchors the absolute deadline.
	CreatedAt time.Time
	// LastSeen anchors the idle deadline.
	LastSeen time.Time
	// AbsoluteExpiry is the ceiling — CreatedAt + the store's absolute
	// timeout. Nothing extends it.
	AbsoluteExpiry time.Time
	// IdleExpiry is LastSeen + the store's idle timeout. NewSessionValue clamps
	// to AbsoluteExpiry, so a store cannot accidentally publish a sliding
	// window that outlives the ceiling.
	IdleExpiry time.Time
	// Data is the caller's payload; it is copied, so the caller keeps
	// ownership of the map it passed.
	Data map[string]string
}
