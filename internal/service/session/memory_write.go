// Package session — the in-process store's writing half.
package session

import (
	"context"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// Save persists the session's data. It never writes the subject.
func (m *memoryStore) Save(_ context.Context, session coresession.SessionValue) error {
	//: the zero session names nothing.
	if session.ID().IsZero() {
		//: InvalidID.
		return coresession.InvalidID
	}
	data := session.Data()
	//: bound the payload before it is stored, not after.
	if boundErr := boundPayload(data); boundErr != nil {
		//: PayloadTooLarge.
		return boundErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.win.clk.Now()
	rec, liveErr := m.liveLocked(session.ID(), now)
	//: NotFound or Expired.
	if liveErr != nil {
		//: nothing is written.
		return liveErr
	}
	//: THE fixation guard. A value naming a different subject is refused, not
	//: applied and not silently reduced to a data-only write: silently
	//: ignoring it would leave the caller believing they had logged the user
	//: in. Regenerate is the operation that was wanted.
	if session.Subject() != rec.subject {
		//: FixationRefused. The subjects are compared with == rather than in
		//: constant time on purpose: a subject is a principal name, not a
		//: bearer secret, and whoever reaches this line already holds the
		//: session's identifier.
		return wrapAs(coresession.FixationRefused, nil)
	}
	rec.data = data
	m.records[rec.digest] = rec
	//: stored.
	return nil
}

// Regenerate rotates the identifier, carries the data across, and binds
// subject. See rotate.go for the lifetime rule.
func (m *memoryStore) Regenerate(_ context.Context, current coresession.ID, subject string) (session coresession.SessionValue, err error) {
	//: the zero identifier names nothing.
	if current.IsZero() {
		//: InvalidID.
		return coresession.SessionValue{}, coresession.InvalidID
	}
	next, mintErr := mintID(m.source)
	//: minted BEFORE the lock, so a slow or blocking entropy source does not
	//: hold every other session's operations behind it.
	if mintErr != nil {
		//: EntropyFailed — the old record is untouched.
		return coresession.SessionValue{}, mintErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.win.clk.Now()
	rec, liveErr := m.liveLocked(current, now)
	//: a session that has already expired is not silently re-authenticated.
	if liveErr != nil {
		//: NotFound or Expired.
		return coresession.SessionValue{}, liveErr
	}
	nextDigest := next.Digest()
	//: a collision here would leave the caller on an identifier someone else
	//: already holds — the exact outcome rotation exists to prevent.
	if _, taken := m.records[nextDigest]; taken {
		//: nothing is written and the old record survives.
		return coresession.SessionValue{}, wrapAs(coresession.IdentifierCollision, nil)
	}
	rotated := m.win.rotate(rec, nextDigest, subject, now)
	//: the old identifier stops working in the same critical section in which
	//: the new one starts, so there is no window where both are valid.
	delete(m.records, rec.digest)
	m.records[nextDigest] = rotated
	//: the caller gets the new identifier and must re-issue the cookie.
	return m.win.build(next, rotated)
}

// Destroy removes the session. It is idempotent.
func (m *memoryStore) Destroy(_ context.Context, id coresession.ID) error {
	//: the zero identifier names nothing — and is not an error either, since
	//: "make sure this is gone" is satisfied.
	if id.IsZero() {
		//: nothing to do.
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: delete on an absent key is a no-op: logging out twice is not a fault,
	//: and reporting NotFound would push every caller into ignoring the error.
	delete(m.records, id.Digest())
	//: revocation is immediate — this is what a session has and a token
	//: does not.
	return nil
}

// Sweep drops every expired record. It is the [coresession.Sweeper] sibling:
// reached by type assertion, never by a wider Store.
func (m *memoryStore) Sweep(_ context.Context) (removed int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.win.clk.Now()
	//: one pass, deleting as it goes — Go permits deleting from a map that is
	//: being ranged over, and the entry is simply not revisited.
	for digest, rec := range m.records {
		//: keep what is still usable.
		if m.win.live(rec, now) {
			//: next record.
			continue
		}
		delete(m.records, digest)
		removed++
	}
	//: a memory store cannot fail to sweep; the error exists for the ones that
	//: can, and returning it keeps one signature for every Sweeper.
	return removed, nil
}
