// Package entitlement - the offline fallback: re-presenting a roster this machine
// already authenticated, under exactly the freshness rules that would apply to
// the same bytes coming off the wire.
//
// This reverses half of a documented decision, so it states which half.
// Service used to hold no cache at all, on the grounds that "a disk cache able
// to authorize would let a frozen file (or a frozen clock) keep a revoked
// subject running forever". The frozen FILE is answered here and was never the
// risk it looked like: the cached bytes are the vendor's own signed bundle,
// they go back through ParseBundle on every read, and ParseRoster refuses a
// window wider than RosterLifetime, one that has not opened, and one that has
// closed. A frozen file therefore stops authorising at its own ExpiresAt —
// which is the bound errors.go already advertises for ErrRosterStale ("it
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
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

// cachedBundleName is the file the last authenticated bundle is kept under. It
// carries the published name so an operator reading a cache directory can tell
// at a glance what the file is and where it came from.
const cachedBundleName string = "roster.signed.json"

// cacheDirMode keeps the cache directory owner-only.
const cacheDirMode os.FileMode = 0o700

// cacheFileMode keeps the cached bundle owner-only.
//
// It is hygiene, NOT a control, and the difference matters. The bundle holds
// no secret — it is a world-readable document published over plain HTTPS — so
// nothing is protected by narrowing it; the reason to narrow it anyway is that
// there is no reason to widen it.
//
// Nothing ever reads this mode back. That is deliberate and it is the lesson
// key_windows.go paid for: os.Stat on Windows reports a synthesised 0444/0666
// that carries no access-control meaning, so a POSIX permission gate on this
// file would refuse every cached bundle there, exactly as it once refused every
// private key. The integrity of these bytes comes from the vendor's signature
// and from nowhere else, which is precisely why no such gate is needed.
const cacheFileMode os.FileMode = 0o600

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
func (s *Service) rememberRoster(raw []byte, roster *RosterValue) {
	//: Two independent reasons to keep nothing, merged into one guard because
	//: they have the same effect: no cache is configured at all — which is
	//: what every injected-getter construction gets — or the bundle offered is
	//: OLDER than the one already kept, which teaches this machine nothing it
	//: does not know and would weaken what it does. The second clause is the
	//: ratchet, and it is what checkClock reads.
	if s.cacheDir == "" || (roster != nil && roster.IssuedAt.Before(s.signedHighWaterMark())) {
		//: Keep what is already there.
		return
	}
	//: Best-effort: a cache we cannot write costs the offline fallback and
	//: nothing else, so it must never turn a successful verification into a
	//: refusal. Say so once rather than fail, because the alternative —
	//: silence — turns "offline does not work here" into a mystery on the day
	//: it is discovered, which is the day the network is already down.
	if err := writeCachedBundle(s.cacheDir, raw); err != nil {
		log.Printf("cannot cache the roster (%v); this machine will need the network on every start", err)
	}
}

// writeCachedBundle replaces the cached bundle atomically.
//
// Write-then-rename, not a truncating write in place: a process killed halfway
// through the latter leaves a short file, and a short file is an unparseable
// bundle — so the one thing a crash must not do is destroy the grace window a
// later outage depends on.
//
// os.WriteFile rather than os.CreateTemp: it opens, writes and closes in one
// call and returns the first failure of the three, which is exactly the
// semantics wanted here. A dropped Close is how a full filesystem produces a
// truncated file that then gets renamed over a good cache, and there is no
// descriptor to drop when nothing ever holds one.
//
// The staging name carries the pid rather than being randomised, so two
// concurrent invocations — a hook and a CLI run, which is the ordinary case on
// this project — stage into different files and neither can interleave writes
// into the other's.
func writeCachedBundle(dir string, raw []byte) error {
	//: The cache root may not exist yet on a first run.
	if mkErr := os.MkdirAll(dir, cacheDirMode); mkErr != nil {
		//: Report what could not be created.
		return fmt.Errorf("creating %s: %w", dir, mkErr)
	}

	installed := cachedBundlePath(dir)
	staged := fmt.Sprintf("%s.%d.tmp", installed, os.Getpid())
	//: Mode is applied at creation, so no separate chmod can leave a window
	//: in which the file exists at something wider.
	if writeErr := os.WriteFile(staged, raw, cacheFileMode); writeErr != nil {
		removeBestEffort(staged)
		//: Report the write failure.
		return fmt.Errorf("writing %s: %w", staged, writeErr)
	}
	//: Rename is what makes the replacement atomic for every reader: nobody
	//: ever observes a half-written bundle at the name they look for.
	if renameErr := os.Rename(staged, installed); renameErr != nil {
		removeBestEffort(staged)
		//: Report the rename failure.
		return fmt.Errorf("installing %s: %w", installed, renameErr)
	}
	//: The cache now holds exactly the bytes that authenticated.
	return nil
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
// ParseRoster refuses an over-wide window, one issued in the future and one
// already closed. A tampered copy is a forgery here just as it would be there;
// an expired one is stale here just as it would be there.
//
// Nothing about this path is more permissive than the network path. It is the
// same document, read from somewhere else.
func (s *Service) cachedRoster(now time.Time) (roster *RosterValue, err error) {
	//: A Service with no cache has no fallback; say so rather than compose a
	//: path from an empty directory and read whatever sits at "roster.signed
	//: .json" in the working directory.
	if s.cacheDir == "" {
		//: Nothing cached, and nowhere to look.
		return nil, fmt.Errorf("%w: no roster cache configured", ErrRosterUnreachable)
	}

	path := cachedBundlePath(s.cacheDir)
	raw, readErr := readCappedFile(path)
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
		return nil, fmt.Errorf("%w: cached roster at %s is not a regular file", ErrRosterUnreachable, path)
	}

	file, openErr := os.Open(path)
	//: Never fetched successfully, or the cache was cleared.
	if openErr != nil {
		//: Report the absent cache.
		return nil, fmt.Errorf("%w: no cached roster at %s: %w", ErrRosterUnreachable, path, openErr)
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
	return fmt.Errorf("%w: clock reads %s, last signed roster was issued %s",
		ErrClockRegressed,
		now.UTC().Format(time.RFC3339),
		mark.UTC().Format(time.RFC3339))
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
