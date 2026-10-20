package session

import (
	"fmt"
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
//
// Every rendering — String, GoString and Format, so every verb — writes the
// session's shape, never its contents. The state sits behind a pointer, so a
// struct holding a SessionValue in an UNEXPORTED field, which fmt prints by
// reflection without calling Format, prints an address rather than the
// identifier, the subject or the data.
type SessionValue struct {
	// _ makes the struct non-comparable at zero cost: with the state behind a
	// pointer, == would otherwise compile and compare allocations.
	_ [0]func()
	// state holds every field behind a pointer, so fmt walking an unexported
	// SessionValue field by reflection prints an address, never the
	// identifier, the subject or the data. It is never mutated after
	// construction — every mutator builds a new one — so copies share it
	// safely. Nil for the zero SessionValue.
	state *sessionState
}

// sessionState is what a SessionValue holds. It is a separate type only so
// that the field a SessionValue carries is a pointer — see the SessionValue
// doc comment.
type sessionState struct {
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
	// expiresAt is the effective deadline: lastSeen + IdleTimeout ALREADY
	// CLAMPED to the absolute ceiling (createdAt + AbsoluteTimeout) by
	// NewSessionValue, the only producer, so ExpiresAt is a read. No operation
	// extends it.
	expiresAt time.Time
	// data is the caller's key/value payload; nil until something is set.
	data map[string]string
}

// zeroSessionState is what the zero SessionValue reads through: every field
// zero. It is never written.
var zeroSessionState sessionState

// view returns the state to read, the shared zero state for the zero
// SessionValue, so no accessor needs a nil guard of its own.
func (s SessionValue) view() *sessionState {
	//: the zero SessionValue holds no state.
	if s.state == nil {
		//: read-only, never handed out of the package.
		return &zeroSessionState
	}
	//: the held, immutable state.
	return s.state
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
	return SessionValue{state: &sessionState{
		id:        state.ID,
		subject:   state.Subject,
		createdAt: state.CreatedAt,
		lastSeen:  state.LastSeen,
		expiresAt: idle,
		data:      maps.Clone(state.Data),
	}}, nil
}

// ID reports the session's identifier. It is a bearer secret: see [ID].
func (s SessionValue) ID() ID {
	//: ID's own redaction protects it from here on.
	return s.view().id
}

// Subject reports the authenticated principal, or "" when the session is
// anonymous. It changes only through [Store.Regenerate].
func (s SessionValue) Subject() string {
	//: read-only; there is deliberately no setter.
	return s.view().subject
}

// CreatedAt reports when this identifier was minted. Regeneration mints a new
// identifier, so it also restarts this clock — and with it the absolute
// deadline. That is intended: a session that just changed principal is a new
// session in every sense that matters.
func (s SessionValue) CreatedAt() time.Time {
	//: the absolute-deadline anchor.
	return s.view().createdAt
}

// LastSeen reports the instant the idle window last slid, i.e. the most recent
// successful [Store.Load].
func (s SessionValue) LastSeen() time.Time {
	//: the idle-deadline anchor.
	return s.view().lastSeen
}

// ExpiresAt reports the EFFECTIVE deadline: the earlier of the absolute
// ceiling and the sliding idle window. The two are never reconciled by
// averaging or by preferring the later one — the earlier deadline always wins,
// which is what makes the absolute timeout a ceiling rather than a suggestion.
func (s SessionValue) ExpiresAt() time.Time {
	//: a sliding window that outran the ceiling would make the ceiling
	//: decorative; NewSessionValue — the only producer, every mutator copies
	//: what it built — clamps it once, so the stored deadline is already the
	//: earlier of the two.
	return s.view().expiresAt
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
	return s.view().id.IsZero()
}

// Get reads one datum. The second return distinguishes "absent" from "present
// and empty", which a bare "" cannot.
func (s SessionValue) Get(key string) (value string, ok bool) {
	//: a nil map reads as empty, so no guard is needed.
	value, ok = s.view().data[key]
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
	next := *s.view()
	next.data = maps.Clone(next.data)
	//: Clone(nil) is nil, so the first Set has to allocate.
	if next.data == nil {
		//: one entry is the common case for a first write.
		next.data = make(map[string]string, 1)
	}
	next.data[key] = value
	//: the caller keeps the old value if they want it.
	return SessionValue{state: &next}
}

// Delete returns a copy without key. Deleting an absent key is not an error —
// the caller asked for the key to be gone and it is.
func (s SessionValue) Delete(key string) SessionValue {
	next := *s.view()
	next.data = maps.Clone(next.data)
	delete(next.data, key)
	//: the receiver still has the key.
	return SessionValue{state: &next}
}

// Keys reports the data keys in sorted order, so a caller iterating them gets
// the same order on every run and in every process.
func (s SessionValue) Keys() []string {
	//: sorted rather than map order: reproducible output is worth the sort on
	//: a payload this size.
	return slices.Sorted(maps.Keys(s.view().data))
}

// Data returns a copy of the whole payload. It is a copy so a caller cannot
// reach behind the value type and mutate a session another goroutine holds.
func (s SessionValue) Data() map[string]string {
	//: Clone(nil) is nil, which ranges as empty — the caller needs no guard.
	return maps.Clone(s.view().data)
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
	if s.view().subject != "" {
		//: still no name — only the fact.
		bound = "bound"
	}
	//: counts and a deadline: enough to tell an empty session from a full one,
	//: and not enough to identify anybody.
	return "session.Session{" + bound + " keys:" + strconv.Itoa(len(s.view().data)) +
		" expires:" + s.ExpiresAt().UTC().Format(time.RFC3339) + "}"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump every field.
func (s SessionValue) GoString() string {
	//: same shape-only rendering as String.
	return s.String()
}

// Format implements fmt.Formatter, so EVERY verb writes the same shape-only
// rendering as String — %d included, which bypasses String and would
// otherwise print every field. %q writes it quoted.
func (s SessionValue) Format(state fmt.State, verb rune) {
	//: %q quotes, so a surrounding quoted rendering stays well formed.
	if verb == 'q' {
		//: the shape, quoted.
		writeRendering(state, strconv.Quote(s.String()))
		//: nothing else to write.
		return
	}
	//: every other verb writes the bare shape.
	writeRendering(state, s.String())
}
