// Package session — the deadline policy shared by every store.
package session

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// window is the validated expiry policy: an idle window, an absolute ceiling,
// and the clock both are read against. Every store embeds one, so the two
// backends cannot disagree about when a session dies.
//
// # When the two deadlines contradict each other
//
// They contradict constantly — that is the normal case, not an edge case. A
// session created at T with a 30-minute idle window and a 12-hour ceiling has
// an idle deadline of T+30m and an absolute deadline of T+12h, and after eleven
// hours of steady use the idle deadline is beyond the ceiling. The rule is one
// sentence and it has no exceptions:
//
//	the EARLIER deadline always wins.
//
// Mechanically that is enforced twice, on purpose. The sliding window is
// CLAMPED to the ceiling when a record is touched (see [window.slide]), so no
// stored session ever carries an idle deadline past its ceiling; and
// core/session.NewSessionValue clamps again when it builds the value, so the rule
// also holds for a session a third-party Store constructed. A store that
// clamped only on write would still be correct here; the second clamp is what
// makes the invariant true of the TYPE rather than of this package.
type window struct {
	// idle is how long a session may go untouched. Validated positive and
	// strictly less than absolute.
	idle time.Duration
	// absolute is the ceiling from the identifier's minting instant.
	absolute time.Duration
	// clk reads time. Never waits on it — a store has nothing to wait for.
	clk clock.Clock
}

// slide returns rec touched at now: its idle deadline restarts from now,
// clamped to the ceiling it may not cross.
func (w window) slide(rec record, now time.Time) record {
	//: the idle anchor moves; the creation anchor never does.
	rec.lastSeen = now
	//: the clamp is applied here so nothing past the ceiling is ever WRITTEN;
	//: NewSessionValue clamps again on read, for values built elsewhere.
	if rec.lastSeen.Add(w.idle).After(rec.absoluteExpiry) {
		//: park lastSeen at the latest instant whose idle deadline is exactly
		//: the ceiling, so the stored record cannot outlive it.
		rec.lastSeen = rec.absoluteExpiry.Add(-w.idle)
	}
	//: a touched record.
	return rec
}

// deadline reports rec's effective expiry — the earlier of the two.
func (w window) deadline(rec record) time.Time {
	idleAt := rec.lastSeen.Add(w.idle)
	//: the ceiling wins whenever the sliding window would reach past it.
	if idleAt.After(rec.absoluteExpiry) {
		//: the absolute deadline.
		return rec.absoluteExpiry
	}
	//: the idle deadline.
	return idleAt
}

// live reports whether rec is still usable at now. The boundary is exclusive:
// a session is dead AT its deadline, not one tick after it.
func (w window) live(rec record, now time.Time) bool {
	//: matches SessionValue.LiveAt so the store and the value never disagree.
	return now.Before(w.deadline(rec))
}

// mint builds a brand-new record for id at now, with the given subject.
func (w window) mint(digest, subject string, now time.Time) record {
	//: creation stamps the ceiling once; nothing extends it afterwards.
	return record{
		digest:         digest,
		subject:        subject,
		createdAt:      now,
		lastSeen:       now,
		absoluteExpiry: now.Add(w.absolute),
		data:           nil,
	}
}

// build converts a stored record into the immutable value the port returns,
// re-attaching the identifier the CALLER presented — the record never held it.
func (w window) build(id coresession.ID, rec record) (session coresession.SessionValue, err error) {
	//: NewSessionValue copies the data map and clamps the idle deadline again.
	return coresession.NewSessionValue(coresession.StateValue{
		ID:             id,
		Subject:        rec.subject,
		CreatedAt:      rec.createdAt,
		LastSeen:       rec.lastSeen,
		AbsoluteExpiry: rec.absoluteExpiry,
		IdleExpiry:     rec.lastSeen.Add(w.idle),
		Data:           rec.data,
	})
}

// rotate returns rec re-identified under nextDigest and bound to subject.
//
// # Rotating does not buy more time — unless the principal changed
//
// A sliding session under an absolute ceiling can only be kept alive up to that
// ceiling. If every Regenerate restarted the ceiling, a caller rotating on a
// timer would have a session that never dies, and the absolute timeout — the
// one deadline the whole policy rests on — would be decorative. So a rotation
// that keeps the SAME subject keeps the same createdAt and the same
// absoluteExpiry: a new identifier, not a new session.
//
// A rotation that CHANGES the subject is different in kind. Anonymous becoming
// alice, or alice becoming bob, is a privilege boundary: the thing that exists
// afterwards is a new session in every sense that matters, and giving it the
// remaining seconds of the anonymous browsing that preceded it would log a user
// out moments after they signed in. So a principal change restarts both clocks,
// and only a principal change does.
func (w window) rotate(rec record, nextDigest, subject string, now time.Time) record {
	//: a privilege boundary — mint a genuinely new session around the old data.
	if subject != rec.subject {
		fresh := w.mint(nextDigest, subject, now)
		fresh.data = rec.data
		//: new identifier, new createdAt, new ceiling.
		return fresh
	}
	//: same principal: only the identifier changes. The ceiling is untouched,
	//: so rotating on a timer cannot extend the session's life.
	rec.digest = nextDigest
	//: the idle window still slides, clamped to the unchanged ceiling.
	return w.slide(rec, now)
}
