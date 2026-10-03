// Package session — the on-disk store.
package session

import (
	"cmp"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresession "github.com/kitsunium/sdk/internal/core/security/session"
	"github.com/kitsunium/sdk/internal/kernel/clock"
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

// tempPrefix starts the name of the temporary file a record is written to
// before it is renamed into place. It carries no record suffix, so a sweep
// never mistakes one for a session — and it lives in the SAME directory,
// because rename(2) is only atomic within one filesystem.
const tempPrefix string = ".tmp-"

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

// kindHeldReplaced names the one PathRedirected shape that involves no link:
// Dir stopped naming the directory the store had just opened.
const kindHeldReplaced string = "replaced"

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
//   - ONE DIRECTORY, HELD. The directory is opened once, at construction, as
//     an os.Root, after an audit of every component of its path that refuses
//     a link anybody could have planted (chain.go). Every name the store
//     touches afterwards — records, temporaries, the lock file, the listing a
//     sweep reads, the directory it flushes — resolves against that handle,
//     so a parent renamed or replaced later moves nothing.
//   - NO OPEN THROUGH A LINK. The two opens that could be redirected — reading
//     a record and opening the lock file — look at the name first and prove
//     the handle afterwards (file_entry.go). A link at a record's name is
//     RecordCorrupt; a link at the lock file is PathRedirected.
//   - ATOMIC PUBLICATION. A record is written to a temporary file in the same
//     directory, synced, and moved into place with rename(2). A reader sees the
//     old record or the new one, never a half-written one, and a failed write
//     leaves the previous record intact rather than publishing an empty file.
//   - DURABLE CHANGES. Every rename and every unlink is followed by an fsync
//     of the directory, because POSIX does not make either survive a crash
//     until the directory itself is flushed — without it a power cut may bring
//     back a record Destroy removed, which is a revocation undone.
//   - SERIALISED READ-MODIFY-WRITE. Every operation runs under one store-wide
//     exclusive flock, so two processes sharing the directory cannot lose an
//     update between a read and the write that follows it.
type fileStore struct {
	// dir is the directory as configured. Nothing is opened through it after
	// construction — root is — and it stays for the fields an error carries
	// and for the tests that observe the directory from outside.
	dir string
	// root is the store directory, held open since construction. Every name
	// resolves against it, so a component of dir renamed or replaced after the
	// store was built cannot move a single record.
	root *os.Root
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
	// gate serialises the read-modify-write cycle BETWEEN GOROUTINES, which
	// the flock above does not. Measured on linux/amd64: flock on the SAME
	// open file description is a lock CONVERSION, not a wait — it succeeds
	// immediately. Since this store holds one descriptor for its whole
	// lifetime (deliberately, see the comment above), every goroutine
	// re-locks that one description and every one of them proceeds. Eight
	// goroutines reached full occupancy of the counted section on every run.
	// Cross-PROCESS exclusion was always intact; cross-goroutine never was,
	// and no test that only spawns processes could see it. Taken BEFORE the
	// flock, the same order internal/service/app/lock's nameGate uses.
	//
	// It is a one-slot CHANNEL rather than a sync.Mutex because a Mutex has no
	// abandonable Lock: a goroutine parked in it cannot be told its caller has
	// gone (ADR 0073).
	gate chan struct{}
	// clk is the time source the poll between flock attempts is armed on, so
	// contention is deterministic under a ManualClock and nothing here sleeps.
	clk clock.Waiter
	// poll is the interval between attempts while another PROCESS holds the
	// lock (FileConfig.Poll).
	poll time.Duration
	// key seals every record.
	key corecrypto.Key
	// win is the validated deadline policy and the clock behind it.
	win window
	// source is the entropy behind every minted identifier.
	source io.Reader
	// syncDir flushes the held directory's entries to the device. It is
	// [flushHeld] over root in production and a field only so that
	// dirsync_internal_test.go can observe WHEN it runs and make it fail: a
	// failing directory fsync cannot be provoked on a real filesystem, and an
	// untested failure path is one that has never run — the reason tempRecord
	// is an interface too.
	syncDir func() error
}

// NewFileStore returns a Store that keeps every session in cfg.Dir.
//
// It refuses, at construction rather than at first use: a configuration it
// cannot honour ([coresession.InvalidConfig]), a platform without the two
// mechanics the guarantees rest on ([coreproc.UnsupportedPlatform]), a
// directory whose permissions expose its contents ([coresession.DirectoryUnsafe]), and a
// location reached through a link anybody could have planted
// ([coresession.PathRedirected]).
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
	//: audit the path, create the directory if absent, narrow it if we
	//: created it, open it, and verify the directory HELD either way.
	root, dirErr := openStoreDir(cfg.Dir)
	//: nothing was opened that outlives this call.
	if dirErr != nil {
		//: StoreUnavailable, DirectoryUnsafe or PathRedirected.
		return nil, dirErr
	}
	//: open the lock last, so a refused store leaves no descriptor behind.
	return openLocked(cfg, root)
}

// openStoreDir audits dir's path, creates the directory if it is absent and
// narrows it if this call created it, and returns it HELD — after asserting
// that the held directory is owner-only and is still the one dir names.
//
// The audit runs FIRST, before anything is created. os.MkdirAll follows an
// indirection at a parent component, so auditing afterwards would mean
// refusing the directory only after having created it inside whatever tree the
// indirection pointed at.
//
// The chmod is conditional, and that condition is the whole point. A directory
// this call CREATED belongs to the store, so narrowing it surprises nobody —
// and it is necessary, because MkdirAll's mode is only a request: a parent
// carrying a default POSIX ACL hands back a group-writable directory whatever
// was asked for, and a store that refused the directory it had just made would
// be unusable on a perfectly ordinary machine. A directory that ALREADY existed belongs to the operator,
// possibly shared with another service, and silently narrowing it is not the
// SDK's call to make; that one is refused instead.
//
// Either way the mode is then ASSERTED — on the HELD directory, through its
// handle — so a filesystem that accepts the chmod without honouring it is
// still caught, and the directory judged is the one every later operation
// uses.
func openStoreDir(dir string) (root *os.Root, err error) {
	//: the components ABOVE the directory — see chain.go.
	if chainErr := checkChain(dir); chainErr != nil {
		//: PathRedirected or StoreUnavailable; nothing was created.
		return nil, chainErr
	}
	_, statErr := os.Stat(dir)
	//: whether the store is about to become the directory's owner.
	created := errors.Is(statErr, fs.ErrNotExist)
	//: umask can only make this stricter; a default ACL can make it looser,
	//: which is why the chmod below exists.
	if mkErr := os.MkdirAll(dir, dirMode); mkErr != nil {
		//: a directory that cannot be created is a backend fault.
		return nil, wrapAs(coresession.StoreUnavailable, mkErr, kerrs.String("op", "mkdir"))
	}
	held, openErr := os.OpenRoot(dir)
	//: a directory that cannot be opened cannot be held.
	if openErr != nil {
		//: StoreUnavailable.
		return nil, wrapAs(coresession.StoreUnavailable, openErr, kerrs.String("op", "open-dir"))
	}
	//: ours to narrow, and only ours — through the handle, so the directory
	//: narrowed is the directory held.
	if created {
		//: MkdirAll's mode is a request; this is what makes it true.
		if chmodErr := narrowHeld(held); chmodErr != nil {
			//: StoreUnavailable, and the handle goes back.
			return nil, releaseDir(held, wrapAs(coresession.StoreUnavailable, chmodErr, kerrs.String("op", "chmod-dir")))
		}
	}
	//: and verify, because a chmod that reports success is not proof.
	if heldErr := assertHeldDir(held, dir); heldErr != nil {
		//: DirectoryUnsafe, PathRedirected or StoreUnavailable.
		return nil, releaseDir(held, heldErr)
	}
	//: one owner-only directory, held, and named by dir.
	return held, nil
}

// narrowHeld narrows the held directory to [dirMode] with fchmod(2) on a
// handle to it.
//
// Not os.Root.Chmod("."): that is fchmodat with AT_SYMLINK_NOFOLLOW, which
// Linux implements only from 6.6 on — before it, go1.27 falls back to
// /proc/self/fd and answers EOPNOTSUPP where /proc is not mounted, which would
// refuse every directory the store creates in such a sandbox. fchmod on a
// descriptor needs neither the flag nor /proc, on every kernel here.
func narrowHeld(root *os.Root) error {
	handle, openErr := root.Open(".")
	//: a directory that cannot be opened cannot be narrowed.
	if openErr != nil {
		//: the caller reports StoreUnavailable.
		return openErr
	}
	chmodErr := handle.Chmod(dirMode)
	closeErr := handle.Close()
	//: the chmod failure wins; a close failure is the answer only on its own.
	return cmp.Or(chmodErr, closeErr)
}

// assertHeldDir refuses the directory the store HOLDS when any account but the
// owner can reach it, and proves dir still names it.
//
// The mode is read through the handle, not the path, so the directory judged
// is the one every later operation resolves against. The path is then stat'ed
// once more and must lead to that same directory: between the path checks
// above and os.OpenRoot, anyone able to rename a component could have handed
// the store a directory nobody checked.
func assertHeldDir(root *os.Root, dir string) error {
	held, statErr := root.Stat(".")
	//: a directory that cannot be stat'ed cannot be vouched for.
	if statErr != nil {
		//: a backend fault.
		return wrapAs(coresession.StoreUnavailable, statErr, kerrs.String("op", "stat-dir"))
	}
	//: any group or world bit is a refusal. Repairing it with a chmod would
	//: hide the fact that the records were exposed for however long it took to
	//: notice, and would silently succeed on a filesystem that ignores the call.
	if held.Mode().Perm()&^dirMode != 0 {
		//: DirectoryUnsafe, without naming the path in the Public string.
		return wrapAs(coresession.DirectoryUnsafe, nil, kerrs.String("want", dirMode.String()))
	}
	named, namedErr := os.Stat(dir)
	//: the path names another directory now, or nothing.
	if namedErr != nil || !os.SameFile(held, named) {
		//: PathRedirected: no retry helps, a human looks at the path.
		return wrapAs(coresession.PathRedirected, namedErr,
			kerrs.String("path", dir), kerrs.String("dir", dir), kerrs.String("kind", kindHeldReplaced))
	}
	//: owner-only, and the one dir names.
	return nil
}

// releaseDir closes a held directory on a construction path that is abandoning
// it, and returns the failure that caused it to be abandoned. A close that
// fails too is subordinate to that failure.
func releaseDir(root *os.Root, cause error) error {
	//: the cause wins; the close failure is the answer only without one.
	if closeErr := root.Close(); closeErr != nil {
		//: subordinate.
		return firstFailure(cause, closeErr, "dir-close")
	}
	//: released.
	return cause
}

// openLocked opens the store-wide lock file through the held directory and
// assembles the store, which takes ownership of both handles.
//
// The lock descriptor deliberately OUTLIVES this function: it is the lock, and
// a deferred close would release it before the store's first operation. On a
// refusal the held directory is given back, since nothing has taken ownership
// of it.
func openLocked(cfg FileConfig, root *os.Root) (store coresession.Store, err error) {
	lock, lockErr := openLockFile(root, cfg.Dir)
	//: a lock that cannot be opened means no serialisation, so no store.
	if lockErr != nil {
		//: StoreUnavailable or PathRedirected, with the directory released.
		return nil, releaseDir(root, lockErr)
	}
	//: crypto/rand.Reader and a real fsync, always, in production. Tests reach
	//: the fields.
	return &fileStore{
		dir: cfg.Dir, root: root, lock: lock, key: cfg.Key, gate: make(chan struct{}, 1),
		clk: cfg.waiter(), poll: cfg.pollInterval(),
		win: cfg.window(), source: rand.Reader,
		syncDir: func() error {
			//: the held directory, never a path that could have moved.
			return flushHeld(root)
		},
	}, nil
}

// openLockFile opens the store-wide lock file, refusing a link planted at its
// name.
//
// The name is fixed and therefore PREDICTABLE, which is the whole of what a
// planter needs: before this, a dangling link at it made the open create the
// link's target and take the lock there — measured, see file_entry.go. The
// open goes through [openEntry], so a link is refused before anything is
// created through it. It is 0600 like every other file here; the lock file's
// contents are empty but its existence should not be readable by another
// account either.
func openLockFile(root *os.Root, dir string) (lock *os.File, err error) {
	opened, found, openErr := openEntry(root, lockName, os.O_CREATE|os.O_RDWR, fileMode)
	//: a directory or a FIFO in the lock file's place redirects nothing; the
	//: lock simply cannot be taken there. The verdict is read before the
	//: error, which is at most a failed close riding along.
	if found == kindNotRegular {
		//: StoreUnavailable, naming what was found.
		return nil, wrapAs(coresession.StoreUnavailable, openErr, kerrs.String("op", "lock-open"), kerrs.String("kind", found))
	}
	//: a link, or a name swapped while it was being opened.
	if found != "" {
		//: PathRedirected: no retry helps.
		return nil, wrapAs(coresession.PathRedirected, openErr,
			kerrs.String("path", filepath.Join(dir, lockName)), kerrs.String("dir", dir), kerrs.String("kind", found))
	}
	//: the medium failed.
	if openErr != nil {
		//: a backend fault, retryable.
		return nil, wrapAs(coresession.StoreUnavailable, openErr, kerrs.String("op", "lock-open"))
	}
	//: the store owns it from here.
	return opened, nil
}

// Close releases the lock descriptor and the held directory. It satisfies
// io.Closer, which is the second capability reached by type assertion rather
// than by a wider Store (ADR 0039) — and it needs no new interface at all,
// because the stdlib already has the right one:
//
//	if closer, ok := store.(io.Closer); ok { _ = closer.Close() }
func (f *fileStore) Close() error {
	var released error
	//: closing releases any flock held on this description as a side effect.
	if lockErr := f.lock.Close(); lockErr != nil {
		//: a failed close is still a backend fault worth surfacing.
		released = wrapAs(coresession.StoreUnavailable, lockErr, kerrs.String("op", "lock-close"))
	}
	//: the directory goes too, whatever the lock said.
	if rootErr := f.root.Close(); rootErr != nil {
		//: subordinate to a lock-close failure, the answer without one.
		released = firstFailure(released, rootErr, "dir-close")
	}
	//: the records stay on disk; Close ends this process's use of them.
	return released
}

// recordName maps a digest to its file's name in the held directory, refusing
// anything that is not the exact shape ID.Digest produces.
func recordName(digest string) (name string, err error) {
	//: the digest is always 64 lowercase hex characters. Checking it before it
	//: becomes a name means no value from outside this package can ever steer
	//: one, whatever a future caller does with the Store port.
	if !isDigest(digest) {
		//: InvalidID rather than a filesystem error.
		return "", coresession.InvalidID
	}
	//: one flat directory: a session store holds live sessions, not an archive.
	return digest + recordSuffix, nil
}

// isDigest reports whether s has the exact shape ID.Digest produces: 64
// lowercase hexadecimal characters. It is the ONE test both recordName and
// recordDigest apply, so building a record's name and recognising a record's
// file cannot drift apart — and what a sweep recognises is what it deletes.
func isDigest(s string) bool {
	//: the length first; it is the cheap half.
	if len(s) != digestLen {
		//: not a SHA-256 in hex.
		return false
	}
	//: then every character: hex.EncodeToString writes only 0-9 and a-f.
	for i := range len(s) {
		//: an uppercase letter is hex too, but never Digest's.
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			//: not a digest this store wrote.
			return false
		}
	}
	//: a digest's shape, and nothing looser.
	return true
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
	name, nameErr := recordName(digest)
	//: a malformed digest never reaches the filesystem.
	if nameErr != nil {
		//: InvalidID.
		return record{}, nameErr
	}
	raw, found, readErr := readEntry(f.root, name)
	//: a directory or a FIFO in a record's place: never opened, so a FIFO
	//: cannot park this read while the store-wide lock is held. The verdict
	//: is read before the error, which is at most a failed close riding along.
	if found == kindNotRegular {
		//: StoreUnavailable, as reading a directory always answered.
		return record{}, wrapAs(coresession.StoreUnavailable, readErr, kerrs.String("op", "read"), kerrs.String("kind", found))
	}
	//: a link at the record's name — never read through — or a name swapped
	//: while it was being opened. Tampering with the directory, and the same
	//: one verdict as tampering with the file.
	if found != "" {
		//: RecordCorrupt, undistinguished.
		return record{}, wrapAs(coresession.RecordCorrupt, readErr)
	}
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
		return record{}, wrapAs(coresession.RecordCorrupt, nil)
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
		return record{}, wrapAs(coresession.RecordCorrupt, nil)
	}
	//: a live, authenticated record.
	return rec, nil
}

// writeLocked seals rec and publishes it atomically. The caller MUST hold the
// store lock.
func (f *fileStore) writeLocked(rec record) error {
	name, nameErr := recordName(rec.digest)
	//: the destination is resolved BEFORE the sealing it would fund — the same
	//: bounds-before-work order the frame decoder uses.
	if nameErr != nil {
		//: InvalidID; nothing was encrypted and nothing was written.
		return nameErr
	}
	box, sealErr := corecrypto.Seal(sealAlgorithm, f.key, encodeRecord(rec), aad(rec.digest))
	//: a seal failure means the AEAD or the key is unusable.
	if sealErr != nil {
		//: StoreUnavailable, with the cause as a field.
		return wrapAs(coresession.StoreUnavailable, sealErr, kerrs.String("op", "seal"))
	}
	//: generate beside, then switch by rename(2).
	if publishErr := f.publish(name, box); publishErr != nil {
		//: nothing was published; the previous record is intact.
		return publishErr
	}
	//: and make the switch survive a power cut.
	return f.flushLocked("sync-dir-publish")
}

// flushLocked makes the directory's latest renames and unlinks durable. The
// caller MUST hold the store lock.
//
// POSIX does not require a rename or an unlink to survive a crash until the
// containing directory is flushed: after a power cut the filesystem may
// legally present the directory as it was before, which for Destroy means the
// revoked session is back. A failure here arrives AFTER the change is visible
// to every reader, so it is reported as StoreUnavailable and deliberately NOT
// rolled back — undoing a rename to repair a durability problem would be a
// second write that can fail the same way (ADR 0056 D7).
func (f *fileStore) flushLocked(op string) error {
	//: the op field says which change is visible but not yet durable.
	if syncErr := f.syncDir(); syncErr != nil {
		//: StoreUnavailable — retryable, and a retry flushes again.
		return wrapAs(coresession.StoreUnavailable, syncErr, kerrs.String("op", op))
	}
	//: durable.
	return nil
}

// publish writes payload to a temporary file in the held directory and moves
// it into place under name.
//
// Same directory, because rename(2) is only atomic within a filesystem and a
// temp file elsewhere would degrade to a copy. Synced before the rename,
// because an atomic rename over unflushed data is atomic about nothing after a
// power cut. And on EVERY failure path the temporary file is removed and the
// previous record is left exactly as it was: a failed write must never replace
// a good record with an empty one.
//
// The temporary is created O_EXCL, which never follows a link — a name nobody
// has taken is the only name it will create — and the rename replaces whatever
// entry holds name, a planted link included, rather than following it.
//
// The directory flush that makes the rename itself durable is the caller's
// next step ([fileStore.flushLocked]), and it is outside this function on
// purpose: the cleanup below is for failures BEFORE the switch, and a flush
// that fails after it has nothing to clean up and nothing to undo.
func (f *fileStore) publish(name string, payload []byte) (err error) {
	tmpName := tempPrefix + rand.Text()
	tmp, createErr := f.root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
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
		err = removeTemp(f.root, tmpName, err)
	}()
	//: assert the mode we were given, rather than assuming the create's 0600
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
	if renameErr := f.root.Rename(tmpName, name); renameErr != nil {
		//: StoreUnavailable; the deferred cleanup removes the orphan.
		return wrapAs(coresession.StoreUnavailable, renameErr, kerrs.String("op", "rename"))
	}
	//: published.
	return nil
}
