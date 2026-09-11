// Package session — the on-disk store.
package session

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// dirMode is the only mode a session directory may have: owner-only. Anything
// with a group or world bit is refused rather than repaired, because repairing
// it would hide the fact that the records were exposed until now.
const dirMode fs.FileMode = 0o700

// fileMode is the only mode a session record may have. It is asserted after
// creation, not merely requested — see [fileStore.publish].
const fileMode fs.FileMode = 0o600

// recordSuffix names a record file. The stem is ID.Digest(), never the
// identifier: a directory listing is not a secret, and on many systems neither
// is a filename in a backup, an audit log or a crash report.
const recordSuffix string = ".session"

// lockName is the store-wide lock file, created once and never removed.
const lockName string = ".lock"

// tempPattern names the temporary file a record is written to before it is
// renamed into place. It carries no record suffix, so a sweep never mistakes
// one for a session — and it lives in the SAME directory, because rename(2) is
// only atomic within one filesystem.
const tempPattern string = ".tmp-*"

// digestLen is the character length of a hex SHA-256 — the only shape a record
// filename may have.
const digestLen int = 64

// sealAlgorithm is the AEAD every record is sealed under. It is named here
// rather than configured: the choice of cipher for the SDK's own at-rest
// framing is not the caller's decision, and a configurable one would be a
// downgrade knob.
const sealAlgorithm corecrypto.Algorithm = "aes-256-gcm"

// recordAAD prefixes the additional authenticated data of every record seal.
const recordAAD string = "kitsunium/sdk/session/record/v1|"

// fileStore keeps every record in one directory, one file per session, sealed.
//
// # What it guarantees, and how
//
//   - CONFIDENTIALITY AT REST. The file holds an AEAD box, never a readable
//     record, so an operator with the file has ciphertext and a filename.
//   - NO IDENTIFIER ON DISK. The filename is ID.Digest() and the record inside
//     stores the same digest. Nothing anywhere on disk is a usable cookie.
//   - NO RECORD SUBSTITUTION. The seal binds each record to its own filename
//     through the additional authenticated data, so renaming one record's file
//     to another's digest yields a value that does not open.
//   - OWNER-ONLY PERMISSIONS, ASSERTED. The directory must be 0700 and each
//     record is created 0600 and then STAT'ed to confirm it — which is what
//     catches a filesystem that accepts a mode and does not enforce it.
//   - ATOMIC PUBLICATION. A record is written to a temporary file in the same
//     directory, synced, and moved into place with rename(2). A reader sees the
//     old record or the new one, never a half-written one, and a failed write
//     leaves the previous record intact rather than publishing an empty file.
//   - SERIALISED READ-MODIFY-WRITE. Every operation runs under one store-wide
//     exclusive flock, so two processes sharing the directory cannot lose an
//     update between a read and the write that follows it.
type fileStore struct {
	// dir holds the records and the lock file.
	dir string
	// lock is the descriptor the store-wide flock is taken on. It is opened
	// once at construction and stays open for the store's lifetime, so no
	// operation races another over creating or unlinking it.
	//
	// A store-wide lock is coarser than a per-record one and that is a
	// deliberate trade. Per-record locking needs a lock file per record, and a
	// lock file that is ever unlinked has a well-known race: a process blocked
	// on the old inode acquires it just as another creates and locks a new one,
	// leaving two holders. Never unlinking them leaks a file per session. One
	// lock, held for microseconds per operation, avoids both.
	lock *os.File
	// mu serialises the read-modify-write cycle BETWEEN GOROUTINES, which
	// the flock above does not. Measured on linux/amd64: flock on the SAME
	// open file description is a lock CONVERSION, not a wait — it succeeds
	// immediately. Since this store holds one descriptor for its whole
	// lifetime (deliberately, see the comment above), every goroutine
	// re-locks that one description and every one of them proceeds. Eight
	// goroutines reached full occupancy of the counted section on every run.
	// Cross-PROCESS exclusion was always intact; cross-goroutine never was,
	// and no test that only spawns processes could see it. Taken BEFORE the
	// flock, the same order internal/service/lock's nameGate uses.
	mu sync.Mutex
	// key seals every record.
	key corecrypto.Key
	// win is the validated deadline policy and the clock behind it.
	win window
	// source is the entropy behind every minted identifier.
	source io.Reader
}

// NewFileStore returns a Store that keeps every session in cfg.Dir.
//
// It refuses, at construction rather than at first use: a configuration it
// cannot honour ([coresession.InvalidConfig]), a platform without the two
// mechanics the guarantees rest on ([coreproc.UnsupportedPlatform]), and a
// directory whose permissions expose its contents ([DirectoryUnsafe]).
func NewFileStore(cfg FileConfig) (store coresession.Store, err error) {
	//: timeouts, directory and key are checked before anything touches disk.
	if validationErr := cfg.validate(); validationErr != nil {
		//: InvalidConfig, naming the field.
		return nil, validationErr
	}
	//: ADR 0018: no native mechanic means a typed refusal, never a store that
	//: reports success while providing neither permissions nor locking.
	if !platformNative {
		//: the SDK-wide sentinel for exactly this situation.
		return nil, coreproc.UnsupportedPlatform
	}
	//: create it if absent, narrow it if we created it, and verify either way.
	if dirErr := prepareDir(cfg.Dir); dirErr != nil {
		//: StoreUnavailable or DirectoryUnsafe.
		return nil, dirErr
	}
	//: open the lock last, so a refused store leaves no descriptor behind.
	return openLocked(cfg)
}

// prepareDir creates the store directory if it is absent and asserts that it is
// owner-only.
//
// The chmod is conditional, and that condition is the whole point. A directory
// this call CREATED belongs to the store, so narrowing it surprises nobody —
// and it is necessary, because MkdirAll's mode is only a request: a parent
// carrying a default POSIX ACL hands back a group-writable directory whatever
// was asked for, and a store that refused the directory it had just made would
// be unusable on a perfectly ordinary machine. A directory that ALREADY existed
// belongs to the operator, possibly shared with another service, and silently
// narrowing it is not the SDK's call to make; that one is refused instead.
//
// Either way the mode is then ASSERTED, so a filesystem that accepts the chmod
// without honouring it is still caught.
func prepareDir(dir string) error {
	_, statErr := os.Stat(dir)
	//: whether the store is about to become the directory's owner.
	created := errors.Is(statErr, fs.ErrNotExist)
	//: umask can only make this stricter; a default ACL can make it looser,
	//: which is why the chmod below exists.
	if mkErr := os.MkdirAll(dir, dirMode); mkErr != nil {
		//: a directory that cannot be created is a backend fault.
		return wrapAs(coresession.StoreUnavailable, mkErr, kerrs.String("op", "mkdir"))
	}
	//: ours to narrow, and only ours.
	if created {
		//: MkdirAll's mode is a request; a default ACL on the parent can widen
		//: it, and this is what makes the request true.
		if chmodErr := os.Chmod(dir, dirMode); chmodErr != nil {
			//: StoreUnavailable.
			return wrapAs(coresession.StoreUnavailable, chmodErr, kerrs.String("op", "chmod-dir"))
		}
	}
	//: and verify, because a chmod that reports success is not proof.
	return assertPrivateDir(dir)
}

// openLocked opens the store-wide lock descriptor and assembles the store.
//
// The descriptor deliberately OUTLIVES this function: it is the lock, and a
// deferred close would release it before the store's first operation. The
// defensive defer below is the sink/file precedent — it closes only on a
// failure path, where nothing has taken ownership of the descriptor.
func openLocked(cfg FileConfig) (store coresession.Store, err error) {
	path := filepath.Join(cfg.Dir, lockName)
	//: 0600 like every other file here; the lock file's contents are empty but
	//: its existence should not be readable by another account either.
	lock, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode)
	//: a lock that cannot be opened means no serialisation, so no store.
	if openErr != nil {
		//: a backend fault, retryable.
		return nil, wrapAs(coresession.StoreUnavailable, openErr, kerrs.String("op", "lock-open"))
	}
	//: close-on-error only; the success path hands the descriptor to the store,
	//: which releases it through Close (io.Closer).
	defer func() {
		//: the store owns it from here.
		if err == nil {
			//: nothing to release.
			return
		}
		//: best-effort close. The result is CHECKED rather than discarded so a
		//: close failure cannot vanish — but err already carries the cause the
		//: caller needs, and firstFailure keeps it.
		if closeErr := lock.Close(); closeErr != nil {
			//: subordinate to the failure that brought us here.
			err = firstFailure(err, closeErr, "lock-close")
		}
	}()
	//: crypto/rand.Reader, always, in production. Tests reach the field.
	return &fileStore{
		dir: cfg.Dir, lock: lock, key: cfg.Key,
		win: cfg.window(), source: rand.Reader,
	}, nil
}

// Close releases the lock descriptor. It satisfies io.Closer, which is the
// second capability reached by type assertion rather than by a wider Store
// (ADR 0039) — and it needs no new interface at all, because the stdlib already
// has the right one:
//
//	if closer, ok := store.(io.Closer); ok { _ = closer.Close() }
func (f *fileStore) Close() error {
	//: closing releases any flock held on this description as a side effect.
	if closeErr := f.lock.Close(); closeErr != nil {
		//: a failed close is still a backend fault worth surfacing.
		return wrapAs(coresession.StoreUnavailable, closeErr, kerrs.String("op", "lock-close"))
	}
	//: the records stay on disk; Close ends this process's use of them.
	return nil
}

// assertPrivateDir refuses a directory any account but the owner can reach.
func assertPrivateDir(dir string) error {
	info, statErr := os.Stat(dir)
	//: a directory that cannot be stat'ed cannot be vouched for.
	if statErr != nil {
		//: a backend fault.
		return wrapAs(coresession.StoreUnavailable, statErr, kerrs.String("op", "stat-dir"))
	}
	//: any group or world bit is a refusal. Repairing it with a chmod would
	//: hide the fact that the records were exposed for however long it took to
	//: notice, and would silently succeed on a filesystem that ignores the call.
	if info.Mode().Perm()&^dirMode != 0 {
		//: DirectoryUnsafe, without naming the path in the Public string.
		return wrapAs(DirectoryUnsafe, nil, kerrs.String("want", dirMode.String()))
	}
	//: owner-only.
	return nil
}

// recordPath maps a digest to its file, refusing anything that is not the exact
// shape ID.Digest produces.
func recordPath(dir, digest string) (path string, err error) {
	//: the digest is always 64 hex characters. Checking it before it reaches
	//: filepath.Join means no value from outside this package can ever steer a
	//: path, whatever a future caller does with the Store port.
	if len(digest) != digestLen {
		//: InvalidID rather than a filesystem error.
		return "", coresession.InvalidID
	}
	//: one flat directory: a session store holds live sessions, not an archive.
	return filepath.Join(dir, digest+recordSuffix), nil
}

// aad returns the additional authenticated data binding a record to its own
// filename. Sealing without it would let anyone with write access to the
// directory rename one session's ciphertext onto another's digest and have it
// open — a session-swap that needs no key at all.
func aad(digest string) []byte {
	//: purpose string plus the digest the file is named after.
	return []byte(recordAAD + digest)
}

// readLocked reads and opens one record. The caller MUST hold the store lock.
func (f *fileStore) readLocked(digest string) (rec record, err error) {
	path, pathErr := recordPath(f.dir, digest)
	//: a malformed digest never reaches the filesystem.
	if pathErr != nil {
		//: InvalidID.
		return record{}, pathErr
	}
	raw, readErr := os.ReadFile(path)
	//: an absent file is an absent session, not a backend fault.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: NotFound.
		return record{}, wrapAs(coresession.NotFound, nil)
	}
	//: anything else is the backend failing.
	if readErr != nil {
		//: StoreUnavailable — retryable, 503.
		return record{}, wrapAs(coresession.StoreUnavailable, readErr, kerrs.String("op", "read"))
	}
	//: the AEAD verifies the filename binding and the contents in one step.
	return f.decodeAt(raw, digest)
}

// decodeAt opens a sealed record and checks that it is the one that was asked
// for.
func (f *fileStore) decodeAt(raw []byte, digest string) (rec record, err error) {
	plain, openErr := corecrypto.Open(f.key, raw, aad(digest))
	//: tampering, truncation, the wrong key and a file moved onto another
	//: digest all land here, and all get the same verdict — crypto.Open is
	//: already non-oracle and this keeps it that way.
	if openErr != nil {
		//: RecordCorrupt.
		return record{}, wrapAs(RecordCorrupt, nil)
	}
	rec, decodeErr := decodeRecord(plain)
	//: a frame that does not parse.
	if decodeErr != nil {
		//: RecordCorrupt.
		return record{}, decodeErr
	}
	//: the record names the digest it belongs to; the filename says the same
	//: thing, and this is where the two are compared in constant time.
	if !digestsEqual(rec.digest, digest) {
		//: RecordCorrupt.
		return record{}, wrapAs(RecordCorrupt, nil)
	}
	//: a live, authenticated record.
	return rec, nil
}

// writeLocked seals rec and publishes it atomically. The caller MUST hold the
// store lock.
func (f *fileStore) writeLocked(rec record) error {
	path, pathErr := recordPath(f.dir, rec.digest)
	//: the destination is resolved BEFORE the sealing it would fund — the same
	//: bounds-before-work order the frame decoder uses.
	if pathErr != nil {
		//: InvalidID; nothing was encrypted and nothing was written.
		return pathErr
	}
	box, sealErr := corecrypto.Seal(sealAlgorithm, f.key, encodeRecord(rec), aad(rec.digest))
	//: a seal failure means the AEAD or the key is unusable.
	if sealErr != nil {
		//: StoreUnavailable, with the cause as a field.
		return wrapAs(coresession.StoreUnavailable, sealErr, kerrs.String("op", "seal"))
	}
	//: generate beside, then switch by rename(2).
	return f.publish(path, box)
}

// publish writes payload to a temporary file in the same directory and moves it
// into place.
//
// Same directory, because rename(2) is only atomic within a filesystem and a
// temp file elsewhere would degrade to a copy. Synced before the rename,
// because an atomic rename over unflushed data is atomic about nothing after a
// power cut. And on EVERY failure path the temporary file is removed and the
// previous record is left exactly as it was: a failed write must never replace
// a good record with an empty one.
func (f *fileStore) publish(path string, payload []byte) (err error) {
	tmp, createErr := os.CreateTemp(f.dir, tempPattern)
	//: a temp file that cannot be created is a backend fault.
	if createErr != nil {
		//: StoreUnavailable.
		return wrapAs(coresession.StoreUnavailable, createErr, kerrs.String("op", "create-temp"))
	}
	//: one cleanup for every failure path below, so no branch can forget it.
	//: On success nothing runs here: writeAndSync has already closed the file
	//: (the bytes must reach the device before the rename) and the rename has
	//: moved it away.
	defer func() {
		//: the happy path owns nothing to clean up.
		if err == nil {
			//: published.
			return
		}
		//: fs.ErrClosed is the ordinary case here — writeAndSync closes on its
		//: own success path, so a rename failure reaches this line with a
		//: closed descriptor, and that is not a failure of anything.
		if closeErr := tmp.Close(); closeErr != nil && !errors.Is(closeErr, fs.ErrClosed) {
			//: subordinate to the failure that brought us here.
			err = firstFailure(err, closeErr, "close-temp")
		}
		//: remove the orphan and return the cause unchanged.
		err = removeTemp(tmp.Name(), err)
	}()
	//: assert the mode we were given, rather than assuming CreateTemp's 0600
	//: survived. On a filesystem that does not enforce mode bits — a FAT or
	//: SMB mount, say — this is the check that turns "the store looks fine" into
	//: a refusal, which is the difference between a guarantee and a hope.
	if modeErr := assertPrivateFile(tmp); modeErr != nil {
		//: the deferred cleanup leaves the old record in place.
		return modeErr
	}
	//: write, flush to the device, close — in that order.
	if writeErr := writeAndSync(tmp, payload); writeErr != nil {
		//: the deferred cleanup leaves the old record in place.
		return writeErr
	}
	//: the switch. Until this line the previous record is the live one.
	if renameErr := os.Rename(tmp.Name(), path); renameErr != nil {
		//: StoreUnavailable; the deferred cleanup removes the orphan.
		return wrapAs(coresession.StoreUnavailable, renameErr, kerrs.String("op", "rename"))
	}
	//: published.
	return nil
}
