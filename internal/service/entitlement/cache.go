// Package entitlement - the offline fallback: re-presenting a roster this machine
// already authenticated, under exactly the freshness rules that would apply to
// the same bytes coming off the wire.
//
// This reverses half of a documented decision, so it states which half.
// Service used to hold no cache at all, on the grounds that "a disk cache able
// to authorize would let a frozen file (or a frozen clock) keep a revoked
// subject running forever". The frozen FILE is answered here and was never the
// risk it looked like: the cached bytes are the vendor's own signed bundle,
// they go back through ParseBundle on every read, and coreent.ParseRoster refuses a
// window wider than coreent.RosterLifetime, one that has not opened, and one that has
// closed. A frozen file therefore stops authorising at its own ExpiresAt —
// which is the bound errors.go already advertises for coreent.ErrRosterStale ("it
// bounds how long a revoked client keeps working offline"), a sentence that
// described nothing until this file existed.
//
// The frozen CLOCK is NOT answered, and no local mechanism can answer it. Every
// source of time an offline process can read — the system clock, file mtimes,
// a monotonic counter that dies at reboot — belongs to the party being checked.
// It was not answered before this file either: a captured bundle served from a
// local origin, behind a trust store the holder controls, with the clock parked
// inside its window, ran the binary indefinitely without any cache. What this
// file changes for such a holder is the effort, not the outcome; what it
// changes for an honest one is that a laptop off the network keeps working
// until the last roster it saw expires.
package entitlement

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// cachedBundleName is the file the last authenticated bundle is kept under. It
// carries the published name so an operator reading a cache directory can tell
// at a glance what the file is and where it came from.
const cachedBundleName string = "roster.signed.json"

// cacheDirMode keeps the cache directory owner-only.
const cacheDirMode os.FileMode = 0o700

// DefaultCacheDir returns where the last authenticated roster is kept.
//
// An empty string means "no cache", which disables the offline fallback
// entirely rather than guessing at a location. That is the safe direction: a
// machine that cannot say where its cache lives falls back to requiring the
// network, which is the behaviour this package had before the cache existed.
func (p *ProductValue) DefaultCacheDir() string {
	root, err := os.UserCacheDir()
	//: Without a cache root there is no location to invent; disable the
	//: fallback rather than write somewhere nobody expects.
	if err != nil {
		//: No cache.
		return ""
	}
	//: Return the per-user location.
	return filepath.Join(root, p.cacheDir())
}

// WithCache points the offline fallback at a directory, or disables it with "".
//
// Only NewService sets this in production, and only the test constructors leave
// it empty: a Service built with an injected getter must not touch a real
// filesystem unless the test asked it to.
func (s *Service) WithCache(dir string) *Service {
	s.cacheDir = dir
	//: Return the receiver so construction reads as one expression.
	return s
}

// cachedBundlePath composes the cache file's location.
func cachedBundlePath(dir string) string {
	//: Callers need the exact path to read, write or report.
	return filepath.Join(dir, cachedBundleName)
}

// rememberRoster keeps the exact bytes that just authenticated, and never
// replaces a newer bundle with an older one.
//
// The monotonic guard is what makes this file a RATCHET rather than merely a
// cache, and the ratchet is what checkClock reads. Without it, an origin lagging
// behind another — or an attacker serving a genuine roster from before the
// clock was rolled back — would lower the newest signed instant this machine
// can prove it has seen, which is the one piece of evidence about time a local
// clock cannot forge.
//
// It does not make the mark tamper-proof: somebody writing an older genuine
// bundle into the file directly never comes through here. What it stops is this
// package lowering its own guard, which is the part this package controls.
func (s *Service) rememberRoster(raw []byte, roster *coreent.RosterValue) {
	//: No cache is configured at all, which is what every injected-getter
	//: construction gets. Checked before the guard because there is nothing
	//: to exclude anyone from.
	if s.cacheDir == "" {
		//: Keep what is already there.
		return
	}
	//: Record the instant BEFORE reaching for the guard, because every way
	//: this function can fail to install is a way it would otherwise forget
	//: what it just authenticated: the guard held elsewhere, no guard to be had
	//: on this platform, a cache directory that cannot be written. None of them
	//: un-signs the bytes. See raiseMarkFloor for what this buys and what it
	//: deliberately does not.
	if roster != nil {
		s.raiseMarkFloor(roster.IssuedAt)
	}
	//: The comparison and the replacement are ONE operation or they are a
	//: lost update. Read outside the guard, both writers see the same mark,
	//: both conclude they are newer than it, and whichever renames LAST
	//: decides what the mark becomes — measured at 108 of 400 rounds before
	//: this guard existed.
	s.holdCacheForWrite(func() {
		//: The bundle offered is OLDER than the one already kept, which
		//: teaches this machine nothing it does not know and would weaken
		//: what it does. This is the ratchet, and it is what checkClock
		//: reads. The mark is re-read HERE, inside the exclusion, so that
		//: what it compares against is what the rename below replaces.
		if roster != nil && roster.IssuedAt.Before(s.markWhileHeld()) {
			//: Keep what is already there.
			return
		}
		//: Best-effort: a cache we cannot write costs the offline fallback
		//: and nothing else, so it must never turn a successful verification
		//: into a refusal. Say so once rather than fail, because the
		//: alternative — silence — turns "offline does not work here" into a
		//: mystery on the day it is discovered, which is the day the network
		//: is already down.
		if err := writeCachedBundle(s.cacheDir, raw); err != nil {
			//: The public sentence names no path by design, and this line is
			//: a log an operator owns rather than a wire — so diagnose puts
			//: the step, the directory and what the filesystem said back
			//: underneath it. Without that the line would say only that
			//: something could not be written.
			log.Printf("cannot cache the roster (%v: %s); this machine will need the network on every start", err, diagnose(err))
		}
	})
}

// writeCachedBundle replaces the cached bundle atomically.
//
// Write-then-rename, not a truncating write in place: a process killed halfway
// through the latter leaves a short file, and a short file is an unparseable
// bundle — so the one thing a crash must not do is destroy the grace window a
// later outage depends on.
//
// The staging name is one the KERNEL guarantees nobody else holds, which the
// previous one was not. It used to carry os.Getpid(), so that two concurrent
// invocations — a hook and a CLI run, which is the ordinary case on this
// project — staged into different files. Two goroutines inside one process
// share a pid, so they shared a staging file: each truncated it, each wrote
// its own bytes into it, and one renamed whatever was there at that instant
// over a valid cache. A short bundle finishing inside a long one leaves the
// long one's tail behind, and that is what got installed — measured at 15 of
// 400 rounds, against a doc comment promising the opposite.
//
// os.CreateTemp answers that with an O_EXCL create, so uniqueness is enforced
// rather than hoped for, and its 0600 is the mode this cache wants: applied at
// creation, so no separate chmod can leave a window in which the file exists
// at something wider. The reason this code avoided it before was that "a dropped
// Close is how a full filesystem produces a truncated file that then gets
// renamed over a good cache" — which is answered by not dropping it. stageBundle
// checks the Close and refuses on it.
func writeCachedBundle(dir string, raw []byte) error {
	//: The cache root may not exist yet on a first run.
	if mkErr := os.MkdirAll(dir, cacheDirMode); mkErr != nil {
		//: Report what could not be created.
		return classify(CacheUnwritable, mkErr,
			errs.String("step", "mkdir"),
			errs.String("dir", dir))
	}

	staged, stageErr := stageBundle(dir, raw)
	//: Nothing was installed, and nothing was disturbed.
	if stageErr != nil {
		//: Report the staging failure.
		return stageErr
	}

	installed := cachedBundlePath(dir)
	//: Rename is what makes the replacement atomic for every reader: nobody
	//: ever observes a half-written bundle at the name they look for. It is
	//: also the step Windows refuses while another handle holds the
	//: destination open, which is why readers take the same guard the writer
	//: does — see cache_lock.go.
	if renameErr := os.Rename(staged, installed); renameErr != nil {
		removeBestEffort(staged)
		//: Report the rename failure.
		return classify(CacheUnwritable, renameErr,
			errs.String("step", "rename"),
			errs.String("path", installed))
	}
	//: The cache now holds exactly the bytes that authenticated.
	return nil
}

// stageBundle writes raw to a file in dir that no other writer can be using,
// and returns the path it wrote.
//
// os.CreateTemp creates at 0600, which is the mode this cache wants: owner
// -only, applied at creation, so no separate chmod can leave a window in which
// the file exists at something wider. That is hygiene and NOT a control — the
// bundle is a world-readable document published over plain HTTPS, so nothing
// is protected by narrowing it; the reason to narrow it anyway is that there
// is no reason to widen it. Nothing in this package ever reads the mode back,
// which is the lesson key_windows.go paid for: os.Stat on Windows reports a
// synthesised 0444/0666 carrying no access-control meaning, so a POSIX
// permission gate here would refuse every cached bundle there. The integrity
// of these bytes comes from the vendor's signature and from nowhere else.
// Test_writeCachedBundle_unixInstallsOwnerOnly asserts the mode instead, where
// the concept exists.
//
// One thing the pid-named predecessor did better, named rather than hidden: a
// process killed between the create and the rename leaves a staged file
// behind, and the old name was reused by the next run with that pid while
// these accumulate. Every path this function can RETURN from removes it, so
// only a hard kill inside a window of a few microseconds leaks one, and what
// leaks is a few hundred bytes that no reader ever looks at — cachedBundlePath
// matches one exact name. Sweeping them would mean enumerating the cache
// directory on a read path that currently opens one file by name, and deciding
// by age which of them belong to a live process. That is more machinery, and
// more ways to delete the wrong thing, than the residue justifies.
//
// Both failures are reported, and the order between them is the point: a Write
// that failed says the bytes are not all there, while a Close that failed says
// they may not have reached the disk. Either one makes the staged file unfit
// to rename over a cache that is currently valid, so either one removes it and
// refuses.
func stageBundle(dir string, raw []byte) (path string, err error) {
	file, createErr := os.CreateTemp(dir, cachedBundleName+".*.tmp")
	//: No descriptor, so nothing to clean up.
	if createErr != nil {
		//: Report what could not be staged.
		return "", classify(CacheUnwritable, createErr,
			errs.String("step", "create_temp"),
			errs.String("dir", dir))
	}

	//: Released at the point it was acquired — and its failure REPORTED, not
	//: logged. A Close that failed says the bytes may never have reached the
	//: disk, which is exactly what makes this file unfit to rename over a
	//: cache that is currently valid; a full filesystem surfaces here and
	//: nowhere else. It only overrides a success, because a write failure
	//: already names the larger problem.
	defer func() {
		closeErr := file.Close()
		//: Nothing to add when the caller is already failing.
		if closeErr == nil || err != nil {
			//: Leave the existing outcome alone.
			return
		}
		removeBestEffort(file.Name())
		path, err = "", classify(CacheUnwritable, closeErr,
			errs.String("step", "close"),
			errs.String("path", file.Name()))
	}()

	staged := file.Name()
	//: Nothing partial is ever installed: the staged file is removed and the
	//: rename that would have consumed it never happens.
	if _, writeErr := file.Write(raw); writeErr != nil {
		removeBestEffort(staged)
		//: Report the write failure.
		return "", classify(CacheUnwritable, writeErr,
			errs.String("step", "write"),
			errs.String("path", staged))
	}
	//: A complete bundle, at a name only this call knows.
	return staged, nil
}

// removeBestEffort drops a staging file without masking the caller's error.
func removeBestEffort(path string) {
	//: Best-effort: a leftover staging file is untidy, never unsafe — it is
	//: never the name any reader looks for. Absence is the ordinary outcome
	//: when the write itself is what failed.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove %s: %v", path, err)
	}
}

// cachedRoster re-reads the last bundle this machine authenticated.
//
// The bytes come off a disk the holder controls, so they are exactly as
// untrusted as the ones an origin serves — and they go through exactly the same
// door. ParseBundle checks the vendor signature before decoding anything, then
// coreent.ParseRoster refuses an over-wide window, one issued in the future and one
// already closed. A tampered copy is a forgery here just as it would be there;
// an expired one is stale here just as it would be there.
//
// Nothing about this path is more permissive than the network path. It is the
// same document, read from somewhere else.
func (s *Service) cachedRoster(now time.Time) (roster *coreent.RosterValue, err error) {
	//: A Service with no cache has no fallback; say so rather than compose a
	//: path from an empty directory and read whatever sits at "roster.signed
	//: .json" in the working directory.
	if s.cacheDir == "" {
		//: Nothing cached, and nowhere to look.
		return nil, refuse(coreent.ErrRosterUnreachable,
			errs.String("stage", "read_cache"),
			errs.String("condition", "no cache directory configured, so there is nowhere to look"))
	}

	path := cachedBundlePath(s.cacheDir)
	var raw []byte
	var readErr error
	//: Under the guard on Windows and unguarded elsewhere: holding this file
	//: open is what makes a concurrent refresh's rename fail there, and the
	//: reader is the only party in a position to avoid it. POSIX needs
	//: nothing — see cache_lock_unix.go for what the guard would cost it.
	s.holdCacheForRead(func() { raw, readErr = readCappedFile(path) })
	//: Never fetched successfully, the cache was cleared, or what is there is
	//: too large to be a roster.
	if readErr != nil {
		//: Report the unusable cache.
		return nil, readErr
	}
	//: Signature first, then the window — the same order, in the same
	//: function, as the bytes that arrive over HTTPS.
	return ParseBundle(raw, s.vendor, now)
}

// readCappedFile reads a file under the SAME artefact cap the network fetch
// applies, rather than through os.ReadFile.
//
// This file sits on a disk its holder controls, so its size is chosen by the
// same party the cap exists to bound. Reading it whole before checking anything
// would hand that party a memory-exhaustion lever the wire path has been
// refusing all along — and worse, an over-cap bundle that was correctly signed
// and in window would have been ACCEPTED here while the identical bytes off an
// origin were refused.
func readCappedFile(path string) (raw []byte, err error) {
	//: A REGULAR file, checked before it is opened, and the check is about
	//: hanging rather than about content. Opening a FIFO for reading blocks
	//: until somebody opens the write end — forever, if nobody does — and this
	//: read happens on the clock ratchet, BEFORE any origin is contacted. A
	//: named pipe dropped into the cache directory would therefore hang every
	//: gated command on a machine whose network is perfectly healthy, with no
	//: timeout anywhere to end it. A symlink to a blocking device does the
	//: same. regularFile follows symlinks deliberately, exactly as it does for
	//: the key pair, so a cache legitimately linked in from elsewhere still
	//: works while the shapes that could never hold a bundle do not.
	//:
	//: It leaves a TOCTOU window — the path could be swapped between this
	//: check and the open — and that residue is accepted: an attacker with
	//: write access to this directory can simply delete the cache, which costs
	//: the same offline window and takes no race to win.
	if !regularFile(path) {
		//: Report the unusable cache without touching it.
		return nil, refuse(coreent.ErrRosterUnreachable,
			errs.String("stage", "read_cache"),
			errs.String("path", path),
			errs.String("condition", "not a regular file, so opening it could block forever"))
	}

	file, openErr := os.Open(path)
	//: Never fetched successfully, or the cache was cleared.
	if openErr != nil {
		//: Report the absent cache.
		return nil, classify(coreent.ErrRosterUnreachable, openErr,
			errs.String("stage", "read_cache"),
			errs.String("path", path))
	}
	//: The body of closeBestEffort, inlined. KTN-GOROUTINE-DEFER recognises
	//: `x.Close()` and a func literal containing it, never `helper(x)`, so
	//: calling the shared helper here reads to the rule as no defer at all.
	//: The rule's intent is right — a descriptor must be released at the point
	//: it is acquired — so the code is written in the shape that says so,
	//: rather than suppressed.
	defer func() {
		//: Best-effort: a descriptor we cannot release is worth a line, never
		//: a refused licence.
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("close %s: %v", path, closeErr)
		}
	}()

	//: Return the artefact under the shared bound.
	return readBounded(file, path)
}

// signedHighWaterMark returns the newest vendor-signed instant this machine has
// ever authenticated, or the zero time when there is none.
//
// The mark is not a file of its own: it IS the cached bundle's IssuedAt. That
// is what makes it trustworthy — it carries the vendor's signature, so a local
// attacker cannot pin this machine's clock by writing a number, only by
// producing a genuine roster they do not have. A separate plain-text mark would
// have handed them exactly that.
//
// Absence is never an error worth reporting: a machine that has never fetched a
// roster has no evidence about time, which is a fact rather than a fault.
func (s *Service) signedHighWaterMark() time.Time {
	//: No cache means no evidence; the ratchet simply does not apply.
	if s.cacheDir == "" {
		//: Nothing recorded.
		return time.Time{}
	}
	var mark time.Time
	//: Under the guard on Windows only, for the same reason cachedRoster is.
	s.holdCacheForRead(func() { mark = s.markWhileHeld() })
	//: What is on disk can be BEHIND what this process authenticated — an
	//: install that stood down, or one the filesystem refused — and the disk is
	//: not the evidence: the signature is. Take the later of the two.
	if floor := s.markFloorNow(); mark.Before(floor) {
		//: The instant this process authenticated but could not install.
		return floor
	}
	//: The newest signed instant this machine can prove it has seen.
	return mark
}

// raiseMarkFloor moves the in-process floor under the mark forward, and never
// backward.
//
// # What it is for
//
// holdCacheForWrite SKIPS the install when the guard is held elsewhere, and its
// own comment justifies that with "Nothing is lost by standing down: the holder
// is installing, and the next verification caches whatever it fetches". That is
// true of the CACHE and false of the MARK. The holder is installing A roster,
// not THIS one — rememberRoster exists for "an origin lagging behind another",
// which is two DIFFERENT generations in flight — so what the skip drops is the
// distance between them, out of the one number checkClock refuses a rolled-back
// clock against. Measured on windows-latest, run 34782571674: one stand-down,
// logged one line above "the mark ended below the newest generation in 1 of 400
// rounds".
//
// The same hole opens on three other paths that never needed a race to reach
// it: a platform with no file lock at all, where cacheGuard returns nil and two
// goroutines run the compare-and-install unguarded; a cache directory that
// cannot be written; and a write that fails halfway. All four end with this
// process having authenticated an instant it cannot read back.
//
// # Why it cannot refuse a clock it should not
//
// checkClock refuses a clock reading earlier than the mark, so a floor that
// ran ahead of reality would refuse a machine whose clock is correct. It cannot:
// rememberRoster is reached from exactly one place, one line after ParseBundle
// returned, and ParseRoster refuses a roster with `now.Before(IssuedAt)`. So
// every instant that reaches this function was already NOT in the future of the
// clock that admitted it.
//
// # What it deliberately does not do
//
// It does not persist, and it is not consulted by the ratchet's own comparison
// inside rememberRoster. The cached bundle stays the durable mark and stays the
// thing the comparison guards, for two reasons: a floor written to disk would
// be a plain number a local holder could set, which is exactly the forgery
// signedHighWaterMark's doc comment says the scheme avoids by making the mark
// the vendor's own signature; and comparing an offered bundle against the floor
// rather than the disk would REFUSE to install a generation newer than what is
// cached but older than what was authenticated, leaving the offline fallback on
// a staler bundle to protect a number that is already protected here. The
// residue is therefore one process restart wide: after a stand-down, a fresh
// process reads the older generation back until the next successful refresh.
func (s *Service) raiseMarkFloor(issued time.Time) {
	s.markFloorMu.Lock()
	//: Released at the point it was acquired.
	defer s.markFloorMu.Unlock()
	//: Compared and assigned under ONE hold. Reading the floor, releasing, and
	//: assigning would be the same lost update this function exists to close,
	//: reintroduced inside the fix for it.
	if s.markFloor.Before(issued) {
		s.markFloor = issued
	}
}

// markFloorNow reads the in-process floor.
func (s *Service) markFloorNow() time.Time {
	s.markFloorMu.Lock()
	//: Released at the point it was acquired.
	defer s.markFloorMu.Unlock()
	//: The newest instant this process authenticated, installed or not.
	return s.markFloor
}

// markWhileHeld is signedHighWaterMark's body for a caller that already holds
// the cache.
//
// It exists because rememberRoster needs the mark INSIDE the exclusion it took
// to install with, and the guard is not reentrant: taking it twice on one
// goroutine deadlocks. Splitting the read out is what lets the compare and the
// install be one operation, which is the whole of the ratchet's guarantee.
func (s *Service) markWhileHeld() time.Time {
	raw, readErr := readCappedFile(cachedBundlePath(s.cacheDir))
	//: Never fetched, cleared, or too large to be a roster.
	if readErr != nil {
		//: Nothing recorded.
		return time.Time{}
	}
	//: Authenticated but NOT judged for freshness: an expired bundle is the
	//: ordinary state of a cached one, and it is still the vendor's signed
	//: statement about when it was issued.
	roster, authErr := authenticateBundle(raw, s.vendor)
	//: Unsigned, forged or undecodable bytes are evidence of nothing.
	if authErr != nil {
		//: Nothing recorded.
		return time.Time{}
	}
	//: The newest signed instant this machine can prove it has seen.
	return roster.IssuedAt
}

// checkClock refuses a clock that has moved backwards past the newest signed
// instant this machine ever authenticated.
//
// This is the whole of what an offline binary can say about time, and it is
// worth saying. Every deadline in this scheme — the roster window, the subject
// term, the grant's NotAfter — is compared against a clock the holder owns, so
// rolling it back into a captured roster's window authorises indefinitely. A
// vendor-signed IssuedAt is the one timestamp that party cannot forge, and a
// local clock reading earlier than the newest one seen is the observable.
//
// Three things it deliberately is not. It is not freeze detection: a clock
// parked exactly on the mark is indistinguishable from a stopped world, here
// and in every other purely local scheme. It is not tamper-proof: the mark
// lives on a disk its holder can delete, which resets it — at the cost of the
// offline window, since the same file is the cache. And its RESOLUTION is the
// publication interval, not the second: the mark is a roster's IssuedAt, so
// with hourly signing a rollback of under an hour lands above the mark and is
// invisible. None of the three is a reason to skip the other side of the
// ledger, which is rollbacks of days or weeks — the size an attacker actually
// needs to re-enter a captured roster's window.
//
// It refuses ONLINE as well as offline, which is the only placement that means
// anything: the attack it answers is a rolled-back clock fed a genuine old
// roster from a substituted origin, and that path is an online one. The refusal
// is deliberately actionable rather than fatal — setting the clock resolves it.
func (s *Service) checkClock(now time.Time) error {
	mark := s.signedHighWaterMark()
	//: No evidence, or a clock that has not gone backwards.
	if mark.IsZero() || !now.Before(mark) {
		//: Nothing to refuse.
		return nil
	}
	//: Name both instants: "set the clock" is only actionable with a target.
	return refuse(coreent.ErrClockRegressed,
		errs.String("condition", "the local clock reads earlier than the newest signed instant this machine has authenticated"),
		errs.String("clock", now.UTC().Format(time.RFC3339)),
		errs.String("mark", mark.UTC().Format(time.RFC3339)))
}

// checkClockAndTime runs the local ratchet and then, if any is configured, the
// network corroboration.
//
// Local FIRST, and the order is not arbitrary: the ratchet costs a file read
// and answers without anybody's permission, while the network check costs a
// round trip and is silent on most locked-down networks. A machine the ratchet
// can already refuse should not wait on a datagram to be told so.
func (s *Service) checkClockAndTime(now time.Time) error {
	//: The evidence this machine already holds, for free.
	if ratchetErr := s.checkClock(now); ratchetErr != nil {
		//: Propagate the regressed clock.
		return ratchetErr
	}
	//: Then whatever the network will vouch for, if anything.
	return s.checkNetworkTime(now)
}
