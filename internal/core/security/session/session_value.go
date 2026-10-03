// Package session — the immutable session a Store hands back.
package session

import (
	"maps"
	"slices"
	"strconv"
	"time"
)

// SessionValue is one session as a [Store] sees it: an identifier, the subject
// it is bound to, its two deadlines, and the caller's data. Every field is
// unexported and every mutator returns a COPY, so a value handed to two
// goroutines cannot be changed under either of them.
//
// The absence here is load-bearing. There is no WithSubject, no SetSubject and
// no Elevate: the subject is written exactly once, by [Store.Regenerate], and
// that call always mints a new identifier. A caller therefore cannot express
// "keep this identifier and log this user in" — the sentence has no method.
type SessionValue struct {
	// id names the session; it is the presented identifier, never one read
	// back from storage (a store keeps only ID.Digest, see id.go).
	id ID
	// subject is the authenticated principal, or "" for anonymous. Written
	// only by the regeneration path.
	subject string
	// createdAt anchors the absolute deadline and never moves for the life of
	// this identifier.
	createdAt time.Time
	// lastSeen anchors the idle deadline and moves on every Load.
	lastSeen time.Time
	// absoluteExpiry is createdAt + AbsoluteTimeout — the ceiling. It is
	// stamped once and no operation extends it.
	absoluteExpiry time.Time
	// idleExpiry is lastSeen + IdleTimeout, ALREADY CLAMPED to absoluteExpiry
	// by whoever built the value, so ExpiresAt needs no special case.
	idleExpiry time.Time
	// data is the caller's key/value payload; nil until something is set.
	data map[string]string
}

// NewSessionValue builds an immutable [SessionValue] from state. It is the only
// producer of the value type outside this package's own accessors.
//
// It refuses a zero [StateValue.ID] with [InvalidID] — a session that names
// nothing could be Loaded by nobody and Saved over anything — and it clamps
// [StateValue.IdleExpiry] to [StateValue.AbsoluteExpiry] so the "earlier
// deadline wins" rule holds for every value in existence, not only for the ones
// this SDK's own stores built.
func NewSessionValue(state StateValue) (session SessionValue, err error) {
	//: an unnamed session is refused at construction, not at first use.
	if state.ID.IsZero() {
		//: the same verdict a malformed inbound identifier gets.
		return SessionValue{}, InvalidID
	}
	idle := state.IdleExpiry
	//: the ceiling is a ceiling. Clamping here rather than in each store means
	//: a third-party Store cannot publish a session that outlives it.
	if idle.After(state.AbsoluteExpiry) {
		//: the absolute deadline wins whenever the two disagree.
		idle = state.AbsoluteExpiry
	}
	//: copy the payload so the caller's map is not the session's storage.
	return SessionValue{
		id:             state.ID,
		subject:        state.Subject,
		createdAt:      state.CreatedAt,
		lastSeen:       state.LastSeen,
		absoluteExpiry: state.AbsoluteExpiry,
		idleExpiry:     idle,
		data:           maps.Clone(state.Data),
	}, nil
}

// ID reports the session's identifier. It is a bearer secret: see [ID].
func (s SessionValue) ID() ID {
	//: ID's own redaction protects it from here on.
	return s.id
}

// Subject reports the authenticated principal, or "" when the session is
// anonymous. It changes only through [Store.Regenerate].
func (s SessionValue) Subject() string {
	//: read-only; there is deliberately no setter.
	return s.subject
}

// CreatedAt reports when this identifier was minted. Regeneration mints a new
// identifier, so it also restarts this clock — and with it the absolute
// deadline. That is intended: a session that just changed principal is a new
// session in every sense that matters.
func (s SessionValue) CreatedAt() time.Time {
	//: the absolute-deadline anchor.
	return s.createdAt
}

// LastSeen reports the instant the idle window last slid, i.e. the most recent
// successful [Store.Load].
func (s SessionValue) LastSeen() time.Time {
	//: the idle-deadline anchor.
	return s.lastSeen
}

// ExpiresAt reports the EFFECTIVE deadline: the earlier of the absolute
// ceiling and the sliding idle window. The two are never reconciled by
// averaging or by preferring the later one — the earlier deadline always wins,
// which is what makes the absolute timeout a ceiling rather than a suggestion.
func (s SessionValue) ExpiresAt() time.Time {
	//: a sliding window that outran the ceiling would make the ceiling
	//: decorative; the store clamps at write time and this is the guard that
	//: makes the invariant true even for a value built elsewhere.
	if s.idleExpiry.Before(s.absoluteExpiry) {
		//: idle bites first.
		return s.idleExpiry
	}
	//: the ceiling bites first, or they coincide.
	return s.absoluteExpiry
}

// LiveAt reports whether the session is still usable at now. The boundary is
// exclusive: a session is dead AT its deadline, not one tick after it.
func (s SessionValue) LiveAt(now time.Time) bool {
	//: a zero value has a zero deadline and is never live.
	return now.Before(s.ExpiresAt())
}

// IsZero reports whether this is the zero SessionValue — what every failing
// [Store] method returns alongside its error. There is no "invalid, but here
// are the claims anyway" path.
func (s SessionValue) IsZero() bool {
	//: the identifier is the one field no real session lacks.
	return s.id.IsZero()
}

// Get reads one datum. The second return distinguishes "absent" from "present
// and empty", which a bare "" cannot.
func (s SessionValue) Get(key string) (value string, ok bool) {
	//: a nil map reads as empty, so no guard is needed.
	value, ok = s.data[key]
	//: absent keys report ok == false.
	return value, ok
}

// Set returns a copy carrying key=value. The receiver is untouched, so
// threading the result is mandatory:
//
//	session = session.Set("locale", "fr")
//	err = store.Save(ctx, session)
func (s SessionValue) Set(key, value string) SessionValue {
	//: copy-on-write: a shared value must not change under another holder.
	next := s
	next.data = maps.Clone(s.data)
	//: Clone(nil) is nil, so the first Set has to allocate.
	if next.data == nil {
		//: one entry is the common case for a first write.
		next.data = make(map[string]string, 1)
	}
	next.data[key] = value
	//: the caller keeps the old value if they want it.
	return next
}

// Delete returns a copy without key. Deleting an absent key is not an error —
// the caller asked for the key to be gone and it is.
func (s SessionValue) Delete(key string) SessionValue {
	next := s
	next.data = maps.Clone(s.data)
	delete(next.data, key)
	//: the receiver still has the key.
	return next
}

// Keys reports the data keys in sorted order, so a caller iterating them gets
// the same order on every run and in every process.
func (s SessionValue) Keys() []string {
	//: sorted rather than map order: reproducible output is worth the sort on
	//: a payload this size.
	return slices.Sorted(maps.Keys(s.data))
}

// Data returns a copy of the whole payload. It is a copy so a caller cannot
// reach behind the value type and mutate a session another goroutine holds.
func (s SessionValue) Data() map[string]string {
	//: Clone(nil) is nil, which ranges as empty — the caller needs no guard.
	return maps.Clone(s.data)
}

// String implements fmt.Stringer and renders the session's SHAPE, never its
// contents: no identifier, no subject, no data keys, no values.
//
// A session's data is whatever the application decided to keep about a logged-in
// user, and its subject names that user. Rendering either would put both into
// whichever log line happened to use %v — which is how identifying data ends up
// in an aggregator that was never scoped to hold it. The counts are enough to
// tell "the session was empty" from "the session had five keys", which is what
// a %v is usually reaching for.
func (s SessionValue) String() string {
	//: "anon" and "bound" describe the subject without naming it.
	bound := "anon"
	//: a non-empty subject means the session survived a Regenerate.
	if s.subject != "" {
		//: still no name — only the fact.
		bound = "bound"
	}
	//: counts and a deadline: enough to tell an empty session from a full one,
	//: and not enough to identify anybody.
	return "session.Session{" + bound + " keys:" + strconv.Itoa(len(s.data)) +
		" expires:" + s.ExpiresAt().UTC().Format(time.RFC3339) + "}"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump every field.
func (s SessionValue) GoString() string {
	//: same shape-only rendering as String.
	return s.String()
}
