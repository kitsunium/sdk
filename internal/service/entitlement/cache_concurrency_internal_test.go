package entitlement

import (
	"crypto/ed25519"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// concurrentRounds is how many times each probe replays its race.
//
// A concurrency defect that reproduces at all reproduces often here — the
// window between the ratchet's read and its rename is microseconds of file I/O
// and an ed25519 verification — so a round count in the hundreds turns "it can
// happen" into a rate. Both probes below FAILED on their first run against the
// unfixed code at 120/400 and 175/400, which is what makes a zero afterwards
// mean something rather than mean "the race did not land today".
const concurrentRounds int = 400

// paddedBundle signs a valid bundle whose serialised LENGTH varies with pad.
//
// Length is the whole point: two writers staging into one file at the same
// name overwrite each other from offset zero, so equal-length bundles hide the
// damage behind a byte-identical result. A short document finishing inside a
// long one leaves the long one's tail behind it, and that tail is what turns a
// signed roster into unparseable bytes.
func paddedBundle(t *testing.T, priv ed25519.PrivateKey, issued time.Time, pad int) []byte {
	t.Helper()

	//: One subject whose UUID is the padding, so the serialised length varies
	//: with pad while the document stays a well-formed roster.
	return signedBundle(t, priv, coreent.RosterValue{
		IssuedAt:  issued,
		ExpiresAt: issued.Add(9 * time.Hour),
		Subjects: map[string]coreent.SubjectValue{
			strings.Repeat("a", pad): {Fingerprint: "SHA256:padding"},
		},
	})
}

// Test_rememberRoster_concurrentGenerationsNeverLowerTheMark pins the ratchet
// against a LOST UPDATE, which is the one way this package can lower its own
// guard while every individual step behaves.
//
// Test_Service_rememberRoster already pins the comparison sequentially: offered
// an older bundle, the ratchet keeps what it has. That test cannot see the
// defect, because the comparison and the replacement are two separate
// filesystem operations and it never runs a second writer between them. Two
// origins publishing different generations — the ordinary case the ratchet's
// own doc comment names — give exactly that interleaving: both readers see the
// same mark, both decide they are newer than it, and whichever renames LAST
// decides what the mark becomes. When that is the older one, the newest signed
// instant this machine can prove it has seen goes DOWN.
//
// A lowered mark is not a cosmetic loss. checkClock refuses a local clock
// reading earlier than the mark, so lowering it widens the interval a
// rolled-back clock can occupy without being refused — it loosens the anti
// -rollback ratchet by the distance between the two generations, through a
// code path in which nothing failed and nothing was forged.
//
// Counted rather than narrated: the assertion is that the final mark is the
// NEWEST generation offered, in every round.
func Test_rememberRoster_concurrentGenerationsNeverLowerTheMark(t *testing.T) {
	t.Parallel()

	base := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// gap separates the two generations offered concurrently.
		gap    time.Duration
		reason string
	}{
		{
			name:   "two origins one hour apart",
			gap:    time.Hour,
			reason: "the mark may only move forward, whatever order the writers finish in",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			older, newer := base.Add(tt.gap), base.Add(2*tt.gap)
			lowered, refusedNewest, wrongRefusal := 0, 0, 0
			//: Each round is its own cache directory and its own race, so one
			//: round's outcome cannot seed the next.
			for range concurrentRounds {
				dir := t.TempDir()
				svc := (&Service{vendor: vendorPub}).WithCache(dir)
				//: Seed a mark BELOW both generations, so both writers pass
				//: the comparison and the interleaving is what decides.
				if seedErr := writeCachedBundle(dir, paddedBundle(t, vendorPriv, base, 1)); seedErr != nil {
					t.Fatalf("seeding cache: %v", seedErr)
				}

				start := make(chan struct{})
				var wg sync.WaitGroup
				var newestVerdict, oldestVerdict error
				//: Goroutine lifecycle: each remembers one generation into its
				//: own verdict slot and exits; the barrier makes them contend
				//: rather than queue, and the slots are read only after Wait so
				//: neither is a shared write.
				remember := func(issued time.Time, verdict *error) {
					defer wg.Done()
					raw := paddedBundle(t, vendorPriv, issued, 1)
					<-start
					*verdict = svc.rememberRoster(raw)
				}
				wg.Add(2)
				go remember(newer, &newestVerdict)
				go remember(older, &oldestVerdict)
				close(start)
				wg.Wait()

				//: The NEWEST generation is newer than the seed and newer than
				//: its rival, so nothing can supersede it whichever order the
				//: two took the guard in — and a stand-down refuses nothing.
				//: This is an invariant of the race rather than an outcome of
				//: it, which is why it counts rather than being tolerated.
				if newestVerdict != nil {
					refusedNewest++
				}
				//: The oldest may or may not be refused: that IS what the race
				//: decides. What is not optional is WHICH refusal, so a future
				//: change refusing it for some other reason cannot pass as this
				//: one.
				if oldestVerdict != nil && !errors.Is(oldestVerdict, coreent.ErrRosterStale) {
					wrongRefusal++
				}
				//: The mark after the race, whoever renamed last.
				if !svc.signedHighWaterMark().Equal(newer) {
					lowered++
				}
			}

			if lowered != 0 {
				t.Errorf("the mark ended below the newest generation in %d of %d rounds, want 0 (%s)",
					lowered, concurrentRounds, tt.reason)
			}
			if refusedNewest != 0 {
				t.Errorf("the newest generation was REFUSED in %d of %d rounds, want 0 — nothing supersedes the newest document (%s)",
					refusedNewest, concurrentRounds, tt.reason)
			}
			if wrongRefusal != 0 {
				t.Errorf("the older generation was refused with something other than coreent.ErrRosterStale in %d of %d rounds, want 0 (%s)",
					wrongRefusal, concurrentRounds, tt.reason)
			}
		})
	}
}

// refreshRacingAReader runs one round of the race and returns the mark left
// behind — a read through the package's own read path, concurrent with one
// install of raw — together with what the install itself had to say.
//
// It is a function rather than a closure in the loop so that neither the
// Service nor the barrier is captured by a closure created per iteration.
func refreshRacingAReader(svc *Service, raw []byte) (mark time.Time, verdict error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	//: Goroutine lifecycle: waits on the barrier, performs exactly one read
	//: through the guarded path, and exits. Joined before this returns, so it
	//: cannot outlive the round. The barrier is what makes it overlap the
	//: install rather than queue behind it.
	wg.Go(func() {
		<-start
		svc.signedHighWaterMark()
	})
	close(start)
	//: The install may lose to the reader's guard, which is what this round
	//: measures. Its VERDICT is not in doubt either way: raw is a newer
	//: generation than the seeded one, nothing supersedes a newer document, and
	//: a stand-down refuses nothing. It is returned so the caller asserts that
	//: rather than the round quietly tolerating a refusal nobody expected.
	verdict = svc.rememberRoster(raw)
	wg.Wait()
	//: Whatever survived the race, and what the install said about it.
	return svc.signedHighWaterMark(), verdict
}

// Test_rememberRoster_refreshesWhileTheCacheIsBeingRead pins that a refresh
// lands even though another holder is reading the bundle at the same time.
//
// This is the ordinary shape of this package under concurrency, and it is not
// hypothetical: readCappedFile — which BOTH signedHighWaterMark and
// cachedRoster go through — opens the installed file and holds the descriptor
// until it has read it. Two invocations of an entitled binary at once, which
// writeCachedBundle's own doc comment calls "the ordinary case on this
// project", put one inside that read while the other installs a refreshed
// roster over it.
//
// On POSIX the rename is atomic and the reader keeps reading the file it
// opened, so this passes without anything being done about it. On Windows,
// replacing a file another handle holds open is REFUSED, and the refusal is
// silent to the caller — rememberRoster logs it and carries on — so the machine
// stays on a bundle that will expire, and discovers it at the moment the
// network is down. The property is asserted on every platform because it is
// the SDK's, not the kernel's.
//
// The reader here goes through the package's own read path rather than opening
// the file itself, and the distinction is the whole point: that path is what
// holdCacheForRead guards on Windows. A holder OUTSIDE this SDK — an antivirus
// scanner, a backup agent — is not guarded and still breaks the rename; that
// residue is pinned as a platform fact in
// Test_windowsRenameOverAnOpenDestination's second row, not fixed here.
func Test_rememberRoster_refreshesWhileTheCacheIsBeingRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{
			name:   "a reader is inside the bundle when the refresh lands",
			reason: "a refresh that cannot replace a cache being read leaves the machine on a bundle that will expire",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			stale := time.Now().Add(-9 * time.Hour).Truncate(time.Second)
			fresh := time.Now().Truncate(time.Second)
			notRefreshed := 0
			//: Each round is its own cache directory and its own race.
			for range concurrentRounds {
				dir := t.TempDir()
				svc := (&Service{vendor: vendorPub}).WithCache(dir)
				//: The bundle already on disk, which the reader will be inside.
				if seedErr := writeCachedBundle(dir, paddedBundle(t, vendorPriv, stale, 1)); seedErr != nil {
					t.Fatalf("seeding cache: %v", seedErr)
				}

				//: The refresh either landed or was silently dropped.
				mark, verdict := refreshRacingAReader(svc, paddedBundle(t, vendorPriv, fresh, 1))
				//: A newer generation is never REFUSED, whatever the guard does
				//: with the install itself.
				if verdict != nil {
					t.Fatalf("rememberRoster() refused a newer generation: %v (%s)", verdict, tt.reason)
				}
				if !mark.Equal(fresh) {
					notRefreshed++
				}
			}

			if notRefreshed != 0 {
				t.Errorf("the refresh did not replace the stale bundle in %d of %d rounds, want 0 (%s)",
					notRefreshed, concurrentRounds, tt.reason)
			}
		})
	}
}

// Test_writeCachedBundle_concurrentWritersNeverDestroyTheCache pins that two
// writers racing cannot install a bundle neither of them signed.
//
// writeCachedBundle's own doc comment states the property this asserts: "the
// one thing a crash must not do is destroy the grace window a later outage
// depends on". It reached that conclusion for a crash and stopped there. The
// staging name carries os.Getpid() — deliberately, so that two PROCESSES stage
// into different files — and two goroutines inside one process share a pid, so
// they share a staging file: each truncates it, each writes its own bytes into
// it, and one renames whatever is there at that instant over the good cache.
//
// Two outcomes follow, and the second is the serious one. The loser's rename
// fails with ENOENT because the winner already moved the file out from under
// it, which is merely noisy. But a short bundle finishing inside a long one
// leaves the long one's tail in place, and THAT is what gets installed: bytes
// that parse as nothing, over a cache that was valid. The offline grace window
// is gone, and it is gone at the moment the network is down, which is the only
// moment anybody looks for it.
//
// The two bundles differ in length precisely so the tearing is observable;
// equal-length ones overwrite to a byte-identical result and hide it.
func Test_writeCachedBundle_concurrentWritersNeverDestroyTheCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// short and long are the padding lengths of the two racing bundles.
		short, long int
		reason      string
	}{
		{
			name:   "a short bundle and a long one",
			short:  16,
			long:   4096,
			reason: "whatever ends up installed must be a bundle somebody actually signed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			issued := time.Now().Truncate(time.Second)
			reported, destroyed := 0, 0
			//: Each round is its own cache directory and its own race.
			for range concurrentRounds {
				dir := t.TempDir()

				start := make(chan struct{})
				var mu sync.Mutex
				var wg sync.WaitGroup
				//: Goroutine lifecycle: each stages and installs one bundle
				//: and exits; the barrier makes them contend.
				install := func(pad int) {
					defer wg.Done()
					raw := paddedBundle(t, vendorPriv, issued, pad)
					<-start
					writeErr := writeCachedBundle(dir, raw)
					//: A reported failure is the mild symptom; count it so the
					//: message can separate the two.
					if writeErr != nil {
						mu.Lock()
						reported++
						mu.Unlock()
					}
				}
				wg.Add(2)
				go install(tt.short)
				go install(tt.long)
				close(start)
				wg.Wait()

				//: What is installed must authenticate. Anything else is a
				//: cache destroyed by writing to it.
				raw, readErr := readCappedFile(cachedBundlePath(dir))
				//: An unreadable cache is already a destroyed one.
				if readErr != nil {
					destroyed++
					continue
				}
				//: Signature only: freshness is not what this round is about.
				if _, _, authErr := authenticateBundle(raw, vendorPub); authErr != nil {
					destroyed++
				}
			}

			//: The safety property, asserted everywhere: whatever ends up
			//: installed is a bundle somebody signed. The staging collision
			//: this test was written for destroyed the cache in 15 of 400
			//: rounds, and a unique staged name is what removed it.
			if destroyed != 0 {
				t.Errorf("installed bytes failed to authenticate in %d of %d rounds, want 0 (%s)",
					destroyed, concurrentRounds, tt.reason)
			}
			//: Whether both writers SUCCEED is a platform question, and only
			//: about this unguarded primitive — production installs through
			//: holdCacheForWrite, so the two never overlap there. See the two
			//: build-tagged declarations of this constant.
			if unguardedRenameIsCollisionFree && reported != 0 {
				t.Errorf("the write reported failure in %d of %d rounds, want 0 — rename(2) replaces unconditionally, so two writers racing one name both succeed (%s)",
					reported, concurrentRounds, tt.reason)
			}
		})
	}
}

// Test_rememberRoster_keepsTheMarkWhenTheInstallStandsDown reproduces, without
// any race at all, the ONE round in 400 that failed on windows-latest.
//
// Its witness is in run 34782571674's windows job, one line above the failure
// and in the directory the failing round used:
//
//	roster cache at ...\Test_rememberRoster_concurrentGenerationsNeverLowerTheMarktwo_o1362201637\018
//	is held elsewhere; leaving this refresh to the holder
//	cache_concurrency_internal_test.go:145: the mark ended below the newest
//	generation in 1 of 400 rounds, want 0
//
// That line is holdCacheForWrite standing down: the runner stalled past
// cacheLockBudget, the goroutine carrying the NEWER generation could not take
// the guard, and the one carrying the older generation decided the mark. The
// probe above cannot say so, because it can only produce that stall by luck;
// this one produces it by holding the lock, so the same defect is red on every
// platform and in a fixed two seconds.
//
// Standing down was reasoned about and the reasoning is written down in
// holdCacheForWrite: "Nothing is lost by standing down: the holder is
// installing, and the next verification caches whatever it fetches." That is
// true of the CACHE and false of the MARK. The holder is installing A roster,
// not THIS one — rememberRoster's own doc comment exists for "an origin lagging
// behind another" — so what is lost is the distance between the two
// generations, out of the one number checkClock refuses a rolled-back clock
// against.
//
// The assertion is therefore signedHighWaterMark's own contract, verbatim from
// its doc comment: "the newest vendor-signed instant this machine has ever
// authenticated". rememberRoster is only ever reached from rosterFrom, one line
// after ParseBundle returned, so by the time this runs the machine HAS
// authenticated the newer generation.
func Test_rememberRoster_keepsTheMarkWhenTheInstallStandsDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// gap separates the generation on disk from the one offered.
		gap    time.Duration
		reason string
	}{
		{
			name:   "the guard is held while the newer generation arrives",
			gap:    time.Hour,
			reason: "an install this machine skipped is still an instant it authenticated, and checkClock is what reads it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			base := time.Now().Truncate(time.Second)
			older, newer := base.Add(tt.gap), base.Add(2*tt.gap)

			dir := t.TempDir()
			svc := (&Service{vendor: vendorPub}).WithCache(dir)
			//: The generation already installed, which the stand-down leaves in
			//: place.
			if seedErr := writeCachedBundle(dir, paddedBundle(t, vendorPriv, older, 1)); seedErr != nil {
				t.Fatalf("seeding cache: %v", seedErr)
			}

			//: A SECOND locker on the same directory, which is what another
			//: process is: takeFlock opens the lock file per acquisition, so its
			//: flock excludes this one exactly as a separate process's would.
			//: Built through the same constructor cacheGuard uses, so a platform
			//: that has no guard refuses here too rather than pretending.
			holder, lockErr := svclock.NewFileLocker(svclock.FileConfig{Dir: dir, Poll: cacheLockPoll})
			//: UnsupportedPlatform is the ONLY refusal worth skipping on: it is
			//: a standing fact about the GOOS, and there is then no stand-down
			//: to provoke because cacheGuard returns nil too. Every other
			//: refusal — LockDirectoryUnsafe, LockBackendFailed — says this
			//: directory is not what the test assumed, and skipping on those
			//: would be a probe that cannot fail. Unreachable on every lane that
			//: RUNS this test: platformNative is true for linux, darwin, the
			//: three BSDs and windows alike.
			if errors.Is(lockErr, coreproc.UnsupportedPlatform) {
				t.Skipf("no file lock on %s, so cacheGuard returns nil here and there is no stand-down to reproduce", runtime.GOOS)
			}
			//: A failure here is an environment problem, not a test outcome.
			if lockErr != nil {
				t.Fatalf("building the holder's locker: %v", lockErr)
			}
			lease, acquireErr := holder.Acquire(t.Context(), cacheLockName)
			//: A failure here is an environment problem, not a test outcome.
			if acquireErr != nil {
				t.Fatalf("holding the cache guard: %v", acquireErr)
			}

			//: The newer generation arrives while the guard is held elsewhere.
			//: This blocks for cacheLockBudget and then stands down.
			//: The install stands down here by construction, and a stand-down
			//: refuses nothing — see rememberRoster's third lapse.
			if err := svc.rememberRoster(paddedBundle(t, vendorPriv, newer, 1)); err != nil {
				t.Fatalf("rememberRoster() error = %v, want nil — a stand-down is not a refusal (%s)", err, tt.reason)
			}

			//: Give the guard back BEFORE reading, so the read below measures
			//: the mark rather than the contention.
			if releaseErr := lease.Release(t.Context()); releaseErr != nil {
				t.Fatalf("releasing the cache guard: %v", releaseErr)
			}

			//: The control, and it is not decoration: if the install had somehow
			//: LANDED, the assertion below would pass without the stand-down ever
			//: happening, and this test would be a shape that guarantees its own
			//: result. The cache must still hold the older generation.
			if installed := svc.markWhileHeld(); !installed.issued.Equal(older) {
				t.Fatalf("the install was not blocked: the cache holds %s, want the seeded %s — this test measured nothing",
					installed.issued.UTC().Format(time.RFC3339), older.UTC().Format(time.RFC3339))
			}

			//: The property, which the stand-down broke.
			if mark := svc.signedHighWaterMark(); !mark.Equal(newer) {
				t.Errorf("the mark reads %s after authenticating %s, want %s (%s)",
					mark.UTC().Format(time.RFC3339), newer.UTC().Format(time.RFC3339),
					newer.UTC().Format(time.RFC3339), tt.reason)
			}

			//: The BOUNDARY of that property, pinned rather than described. The
			//: floor is per-Service by construction, so a second verifier built
			//: over the same directory reads the disk and nothing else — which
			//: is exactly what a process restart does. Asserting it here is what
			//: keeps "the residue is one Service instance wide" from being a
			//: sentence nobody can check; raiseMarkFloor says why the shared
			//: alternative was refused. Change this line only with that comment.
			if fresh := (&Service{vendor: vendorPub}).WithCache(dir).signedHighWaterMark(); !fresh.Equal(older) {
				t.Errorf("a second Service over the same cache reads %s, want the installed %s — the documented boundary of the in-process floor moved (%s)",
					fresh.UTC().Format(time.RFC3339), older.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}
