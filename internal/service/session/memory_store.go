// Package session — the in-process store.
package session

import (
	"context"
	"crypto/rand"
	"io"
	"sync"
	"time"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// initialRecords is the map's starting capacity. It is a hint, not a bound: a
// memory store holds as many sessions as its process has live users, which
// nothing here can know. Sixty-four is one page's worth of buckets — enough to
// carry a small service without a rehash, cheap enough to be wrong.
const initialRecords int = 64

// memoryStore keeps every record in one map guarded by an RWMutex. It dies
// with the process, which is the whole of its contract: it is the right store
// for a single-process service, for a test, and for a development run, and the
// wrong one for anything that must survive a deploy.
//
// Records are keyed by ID.Digest, never by the identifier. That is not a
// micro-optimisation — a Go map lookup is not constant-time, so keying on the
// secret would make lookup latency depend on the secret. Keying on its digest
// moves the variable-time work onto a value that is not secret, and the
// identifier is then re-checked with crypto/subtle.
type memoryStore struct {
	// mu guards records. RWMutex rather than Mutex because Load is the hot
	// path — but note that Load also WRITES (it slides the idle window), so it
	// takes the write lock; the read lock serves Sweep's scan.
	mu sync.RWMutex
	// records maps ID.Digest() to the stored session.
	records map[string]record
	// win is the validated deadline policy and the clock behind it.
	win window
	// source is the entropy behind every minted identifier. Unexported and
	// absent from Config: see mint.go.
	source io.Reader
}

// NewMemoryStore returns a Store that keeps every session in this process's
// memory. It refuses a Config it cannot honour (ADR 0031) and never returns an
// inert store.
func NewMemoryStore(cfg Config) (store coresession.Store, err error) {
	//: both timeouts and their ordering are refused here, not at first use.
	if validationErr := validateWindow(cfg.IdleTimeout, cfg.AbsoluteTimeout); validationErr != nil {
		//: InvalidConfig, naming the field.
		return nil, validationErr
	}
	//: crypto/rand.Reader, always, in production. Tests reach the field.
	return &memoryStore{
		records: make(map[string]record, initialRecords),
		win:     cfg.window(),
		source:  rand.Reader,
	}, nil
}

// New mints an anonymous session.
//
// The context is unused and named so. This store never blocks, never performs
// I/O and never crosses a process boundary, so there is nothing a cancellation
// could interrupt; honouring ctx here would mean inventing a failure the store
// cannot actually have. The file store, which does block, honours it.
func (m *memoryStore) New(_ context.Context) (session coresession.SessionValue, err error) {
	id, mintErr := mintID(m.source)
	//: no identifier is minted from partial entropy.
	if mintErr != nil {
		//: EntropyFailed.
		return coresession.SessionValue{}, mintErr
	}
	now := m.win.clk.Now()
	rec := m.win.mint(id.Digest(), "", now)
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a collision at 256 bits means the random source is broken. Overwriting
	//: would hand one caller's session to another; refusing is the only safe
	//: answer, and it is loud.
	if _, taken := m.records[rec.digest]; taken {
		//: nothing is written.
		return coresession.SessionValue{}, wrapAs(coresession.IdentifierCollision, nil)
	}
	m.records[rec.digest] = rec
	//: the value carries the identifier; the record never does.
	return m.win.build(id, rec)
}

// Load resolves id and slides its idle window.
func (m *memoryStore) Load(_ context.Context, id coresession.ID) (session coresession.SessionValue, err error) {
	//: the zero identifier names nothing and is refused before any lookup.
	if id.IsZero() {
		//: InvalidID.
		return coresession.SessionValue{}, coresession.InvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.win.clk.Now()
	rec, liveErr := m.liveLocked(id, now)
	//: NotFound or Expired; an expired record has already been dropped.
	if liveErr != nil {
		//: no partial session ever escapes alongside an error.
		return coresession.SessionValue{}, liveErr
	}
	//: sliding IS the idle timeout: a window that is not refreshed on use is
	//: an absolute timeout wearing another name.
	rec = m.win.slide(rec, now)
	m.records[rec.digest] = rec
	//: re-attach the identifier the caller presented.
	return m.win.build(id, rec)
}

// liveLocked resolves id to a live record, dropping it if it has expired. The
// caller MUST hold the write lock.
func (m *memoryStore) liveLocked(id coresession.ID, now time.Time) (rec record, err error) {
	digest := id.Digest()
	rec, found := m.records[digest]
	//: absent, destroyed, or already swept — and, on the second half, a record
	//: whose stored digest does not match the one it was filed under. The
	//: second check can only fail on a corrupted map, and it is here so that
	//: EVERY path from an identifier to a record ends in a crypto/subtle
	//: comparison rather than in a map's word-at-a-time one. Both answer
	//: NotFound: an intruder learns nothing from which of them fired.
	if !found || !digestsEqual(rec.digest, digest) {
		//: NotFound.
		return record{}, wrapAs(coresession.NotFound, nil)
	}
	//: the earlier of the two deadlines decides.
	if !m.win.live(rec, now) {
		//: drop as we report, so the next call answers NotFound and a dead
		//: record cannot be resurrected by a clock that moves backwards.
		delete(m.records, digest)
		//: Expired.
		return record{}, wrapAs(coresession.Expired, nil)
	}
	//: a live record.
	return rec, nil
}
