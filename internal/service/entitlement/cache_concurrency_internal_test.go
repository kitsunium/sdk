package entitlement

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
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

	roster := coreent.RosterValue{
		IssuedAt:  issued,
		ExpiresAt: issued.Add(9 * time.Hour),
		Subjects: map[string]coreent.SubjectValue{
			strings.Repeat("a", pad): {Fingerprint: "SHA256:padding"},
		},
	}
	raw, marshalErr := json.Marshal(roster)
	//: A failure here is an environment problem, not a test outcome.
	if marshalErr != nil {
		t.Fatalf("marshalling roster: %v", marshalErr)
	}
	bundle, bundleErr := json.Marshal(BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if bundleErr != nil {
		t.Fatalf("marshalling bundle: %v", bundleErr)
	}
	//: Return the one document the fallback reads.
	return bundle
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
			lowered := 0
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
				//: Goroutine lifecycle: each remembers one generation and
				//: exits; the barrier makes them contend rather than queue.
				remember := func(issued time.Time) {
					defer wg.Done()
					raw := paddedBundle(t, vendorPriv, issued, 1)
					<-start
					svc.rememberRoster(raw, &coreent.RosterValue{IssuedAt: issued})
				}
				wg.Add(2)
				go remember(newer)
				go remember(older)
				close(start)
				wg.Wait()

				//: The mark after the race, whoever renamed last.
				if !svc.signedHighWaterMark().Equal(newer) {
					lowered++
				}
			}

			if lowered != 0 {
				t.Errorf("the mark ended below the newest generation in %d of %d rounds, want 0 (%s)",
					lowered, concurrentRounds, tt.reason)
			}
		})
	}
}

// Test_writeCachedBundle_installsWhileAReaderHoldsTheCacheOpen pins that a
// refresh lands even though another reader is inside the cached bundle.
//
// This is the ordinary shape of this package under concurrency, and it is not
// hypothetical: readCappedFile — which BOTH signedHighWaterMark and
// cachedRoster go through — opens the installed file and holds the descriptor
// until it has read it. Two invocations of an entitled binary at once, which
// writeCachedBundle's own doc comment calls "the ordinary case on this
// project", put one process inside that read while the other installs a
// refreshed roster over it.
//
// On POSIX the rename is atomic and the reader keeps reading the file it
// opened, so this passes without anything being done about it. It is asserted
// on every platform anyway, because the property is the SDK's, not the
// kernel's — and because the only way to see where it does not hold is to
// state it where it does.
//
// The failure this rules out is silent by construction. rememberRoster treats
// a write failure as best-effort and logs it, so a machine that can never
// refresh its cache looks exactly like a machine that can, right up until the
// network goes down and the stale bundle has expired — the one moment the
// offline grant exists for.
func Test_writeCachedBundle_installsWhileAReaderHoldsTheCacheOpen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{
			name:   "a reader is inside the bundle when the refresh lands",
			reason: "a refresh that cannot replace an open cache leaves the machine on a bundle that will expire",
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

			dir := t.TempDir()
			stale := time.Now().Add(-9 * time.Hour).Truncate(time.Second)
			fresh := time.Now().Truncate(time.Second)
			//: The bundle already on disk, which the reader will hold open.
			if seedErr := writeCachedBundle(dir, paddedBundle(t, vendorPriv, stale, 1)); seedErr != nil {
				t.Fatalf("seeding cache: %v", seedErr)
			}

			//: Exactly how readCappedFile holds the file: an ordinary
			//: os.Open, kept open across the refresh below.
			reader, openErr := os.Open(cachedBundlePath(dir))
			//: A failure here is an environment problem, not a test outcome.
			if openErr != nil {
				t.Fatalf("opening the cached bundle: %v", openErr)
			}
			//: Released at the point it was acquired; the refresh happens
			//: while it is still held, which is the whole point.
			defer func() {
				//: Best-effort: the descriptor's release is not what is under
				//: test, but a failure to release it would mask the result.
				if closeErr := reader.Close(); closeErr != nil {
					t.Errorf("closing the held reader: %v", closeErr)
				}
			}()

			writeErr := writeCachedBundle(dir, paddedBundle(t, vendorPriv, fresh, 1))
			//: The refusal itself, named with the platform's own error, is
			//: the evidence — not a narrative about what Windows does.
			if writeErr != nil {
				t.Fatalf("writeCachedBundle() while a reader holds the cache open = %v, want nil (%s)", writeErr, tt.reason)
			}

			raw, readErr := readCappedFile(cachedBundlePath(dir))
			//: A refresh that reported success must have installed something.
			if readErr != nil {
				t.Fatalf("reading the refreshed cache: %v", readErr)
			}
			installed, authErr := authenticateBundle(raw, vendorPub)
			//: Whatever is installed must still be the vendor's.
			if authErr != nil {
				t.Fatalf("authenticating the refreshed cache: %v", authErr)
			}
			if !installed.IssuedAt.Equal(fresh) {
				t.Errorf("installed bundle IssuedAt = %s, want %s — the refresh did not replace the stale copy (%s)",
					installed.IssuedAt, fresh, tt.reason)
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
				if _, authErr := authenticateBundle(raw, vendorPub); authErr != nil {
					destroyed++
				}
			}

			if destroyed != 0 || reported != 0 {
				t.Errorf("installed bytes failed to authenticate in %d of %d rounds and the write reported failure in %d, want 0 and 0 (%s)",
					destroyed, concurrentRounds, reported, tt.reason)
			}
		})
	}
}
