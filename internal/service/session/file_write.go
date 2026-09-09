// Package session — the file store's writing half.
package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// Save persists the session's data. It never writes the subject.
func (f *fileStore) Save(ctx context.Context, session coresession.SessionValue) error {
	//: the zero session names nothing.
	if session.ID().IsZero() {
		//: InvalidID.
		return coresession.InvalidID
	}
	data := session.Data()
	//: bound the payload before it is sealed, not after.
	if boundErr := boundPayload(data); boundErr != nil {
		//: PayloadTooLarge.
		return boundErr
	}
	//: the whole read-modify-write runs under one lock.
	return f.withLock(ctx, func() error {
		rec, liveErr := f.liveLocked(session.ID(), f.win.clk.Now())
		//: NotFound or Expired.
		if liveErr != nil {
			//: nothing is written.
			return liveErr
		}
		//: THE fixation guard, identical to the memory store's — the two stores
		//: answer the same question the same way, which is what makes the port
		//: a contract rather than a suggestion.
		if session.Subject() != rec.subject {
			//: FixationRefused; Regenerate is the operation that was wanted.
			return wrapAs(coresession.FixationRefused, nil)
		}
		rec.data = data
		//: seal and publish atomically.
		return f.writeLocked(rec)
	})
}

// Regenerate rotates the identifier, carries the data across, and binds
// subject. See window.rotate for the lifetime rule.
func (f *fileStore) Regenerate(ctx context.Context, current coresession.ID, subject string) (session coresession.SessionValue, err error) {
	//: the zero identifier names nothing.
	if current.IsZero() {
		//: InvalidID.
		return coresession.SessionValue{}, coresession.InvalidID
	}
	next, mintErr := mintID(f.source)
	//: minted before the lock.
	if mintErr != nil {
		//: EntropyFailed — the old record is untouched.
		return coresession.SessionValue{}, mintErr
	}
	var rotated record
	lockErr := f.withLock(ctx, func() error {
		var rotateErr error
		rotated, rotateErr = f.rotateLocked(current, next, subject)
		//: propagate the first failure unchanged.
		return rotateErr
	})
	//: any failure leaves the old record exactly as it was.
	if lockErr != nil {
		//: the typed verdict.
		return coresession.SessionValue{}, lockErr
	}
	//: the caller gets the new identifier and must re-issue the cookie.
	return f.win.build(next, rotated)
}

// rotateLocked performs the rotation. The caller MUST hold the store lock.
func (f *fileStore) rotateLocked(current, next coresession.ID, subject string) (rec record, err error) {
	now := f.win.clk.Now()
	live, liveErr := f.liveLocked(current, now)
	//: a session that has already expired is not silently re-authenticated.
	if liveErr != nil {
		//: NotFound or Expired.
		return record{}, liveErr
	}
	//: a collision would leave the caller on an identifier someone else already
	//: holds — the exact outcome rotation exists to prevent.
	if f.exists(next.Digest()) {
		//: IdentifierCollision — the old record survives.
		return record{}, wrapAs(coresession.IdentifierCollision, nil)
	}
	rotated := f.win.rotate(live, next.Digest(), subject, now)
	//: write the NEW record first. If this fails the old identifier still
	//: works, which is a worse security posture than the new one but a better
	//: one than a caller holding an identifier that names no session at all.
	if writeErr := f.writeLocked(rotated); writeErr != nil {
		//: StoreUnavailable.
		return record{}, writeErr
	}
	//: and only then retire the old one, so there is no instant in which
	//: neither identifier resolves.
	if removeErr := f.removeLocked(live.digest); removeErr != nil {
		//: StoreUnavailable — reported, and the caller retries the whole
		//: rotation rather than being told it succeeded.
		return record{}, removeErr
	}
	//: rotated.
	return rotated, nil
}

// Destroy removes the session. It is idempotent.
func (f *fileStore) Destroy(ctx context.Context, id coresession.ID) error {
	//: the zero identifier names nothing, and asking for it to be gone is
	//: already satisfied.
	if id.IsZero() {
		//: nothing to do.
		return nil
	}
	//: revocation is immediate — this is what a session has and a token
	//: does not.
	return f.withLock(ctx, func() error {
		//: an absent file is success.
		return f.removeLocked(id.Digest())
	})
}

// Sweep drops every expired record. It is the [coresession.Sweeper] sibling:
// reached by type assertion, never by a wider Store.
func (f *fileStore) Sweep(ctx context.Context) (removed int, err error) {
	lockErr := f.withLock(ctx, func() error {
		entries, readErr := os.ReadDir(f.dir)
		//: a directory that cannot be listed is a backend fault.
		if readErr != nil {
			//: StoreUnavailable.
			return wrapAs(coresession.StoreUnavailable, readErr, kerrs.String("op", "readdir"))
		}
		//: one pass over the directory; the lock is held throughout, which is
		//: why sweeping is the caller's decision and not a background loop.
		removed = f.sweepEntries(entries)
		//: a sweep never fails on one bad record.
		return nil
	})
	//: the count is meaningful even when the lock failed — it is zero.
	return removed, lockErr
}

// sweepEntries removes every expired or unreadable record. The caller MUST hold
// the store lock.
func (f *fileStore) sweepEntries(entries []os.DirEntry) int {
	now := f.win.clk.Now()
	removed := 0
	//: one pass over the listing; every entry that is not a live record goes.
	for _, entry := range entries {
		digest, ok := recordDigest(entry)
		//: the lock file, a stale temp file, a subdirectory.
		if !ok {
			//: not ours.
			continue
		}
		rec, readErr := f.readLocked(digest)
		//: an unreadable record is swept too: it can never be loaded again, so
		//: leaving it would grow the directory forever. A record made
		//: unreadable by a KEY ROTATION is the same case, and the same answer —
		//: which is why rotating the store key logs everyone out.
		if readErr == nil && f.win.live(rec, now) {
			//: still usable.
			continue
		}
		//: best effort: a file that cannot be removed is retried next sweep.
		if f.removeLocked(digest) == nil {
			removed++
		}
	}
	//: how many are gone.
	return removed
}

// recordDigest maps a directory entry to the digest it names, or reports that
// it is not a record file.
func recordDigest(entry os.DirEntry) (digest string, ok bool) {
	//: subdirectories are not records; the store keeps one flat directory.
	if entry.IsDir() {
		//: skip.
		return "", false
	}
	name := entry.Name()
	//: the suffix is the only marker; the lock file and temp files have none.
	if filepath.Ext(name) != recordSuffix {
		//: skip.
		return "", false
	}
	digest = strings.TrimSuffix(name, recordSuffix)
	//: a file with the right suffix and the wrong stem is not one of ours
	//: either — and refusing it here is what keeps a hand-made filename from
	//: reaching recordPath.
	return digest, len(digest) == digestLen
}
