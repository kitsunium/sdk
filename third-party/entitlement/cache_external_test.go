package entitlement_test

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"

	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// cachedBundleFile is the name the offline fallback looks for. Spelled out
// here rather than imported: an external test that hardcodes it is what stops
// the file being renamed without anybody noticing that every machine in the
// field loses its grace window on the next upgrade.
const cachedBundleFile string = "roster.signed.json"

// plantBundle writes raw where the offline fallback will find it.
//
// Writing it directly rather than letting a successful verification produce it
// is the point of every negative row below: a planted file is the threat model,
// not a convenience.
func plantBundle(t *testing.T, cacheDir string, raw []byte) {
	t.Helper()

	//: The fallback creates this itself in production; a test that plants a
	//: file has to create it first.
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("creating cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, cachedBundleFile), raw, 0o600); err != nil {
		t.Fatalf("planting bundle: %v", err)
	}
}

// TestVerifyFallsBackToTheCachedRoster pins the offline grace window, and in
// particular that it is not a lower bar but the SAME bar applied to a copy.
//
// The package used to hold no cache at all, on the grounds that "a disk cache
// able to authorize would let a frozen file (or a frozen clock) keep a revoked
// subject running forever". The frozen-file half is what these rows answer: a
// cached bundle goes back through ParseBundle on every read, so it stops
// authorising at its own ExpiresAt and a forged one never authorises at all.
// The frozen-CLOCK half is not answered here or anywhere, and no row pretends
// otherwise.
func TestVerifyFallsBackToTheCachedRoster(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// plant describes what sits in the cache before the offline attempt:
		// "" for nothing, "genuine" / "expired" / "forged" for a written one.
		plant string
		// warm runs a successful ONLINE verification first, which is how a
		// real machine acquires its cache.
		warm    bool
		wantErr error
		reason  string
	}{
		{
			name:   "a warm machine keeps working when no origin answers",
			warm:   true,
			reason: "this is the whole feature: a laptop off the network runs until the roster it last saw expires",
		},
		{
			name:    "a machine that never fetched one still cannot start",
			plant:   "",
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "the fallback is a memory of a verification, not a substitute for ever having one",
		},
		{
			name:    "a cached roster past its own window authorises nothing",
			plant:   "expired",
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "a frozen FILE stops at its own ExpiresAt — this is the half of the no-cache objection that is answered",
		},
		{
			name:    "a bundle signed by anyone else authorises nothing",
			plant:   "forged",
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "the cache is untrusted input read off a disk its holder controls; the signature is what makes it a roster",
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

			dir, cacheDir := t.TempDir(), t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			subjects := map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}}
			expiry := now.Add(2 * time.Hour)

			getter := &stubGetter{bundle: signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now.Add(-time.Minute),
				ExpiresAt: expiry,
				Subjects:  subjects,
			})}
			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo")).
				WithCache(cacheDir)

			//: A successful online verification is the ONLY thing that fills
			//: the cache in production, so warming it that way is what makes
			//: this test about the real path.
			if tt.warm {
				if _, warmErr := svc.Verify(now); warmErr != nil {
					t.Fatalf("warming verification: %v", warmErr)
				}
			}
			plantCase(t, plantRequest{
				cacheDir:   cacheDir,
				plant:      tt.plant,
				vendorPriv: vendorPriv,
				subjects:   subjects,
				now:        now,
			})

			//: Every origin goes dark. Nothing local changes.
			getter.fail = true

			grant, err := svc.Verify(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				//: The refusal must name the NETWORK, which is what actually
				//: happened. Pointing an operator at a cache file they have
				//: never heard of sends them to fix the wrong thing.
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			//: Running on a copy is a state the operator has to be able to
			//: see; a grant that cannot say so is indistinguishable from a
			//: fresh one.
			if !grant.Offline {
				t.Error("Verify() grant.Offline = false, want true (the answer came from the cache)")
			}
			//: The grant may not outlive the document that authorised it, and
			//: that document is the cached roster.
			if !grant.NotAfter.Equal(expiry) {
				t.Errorf("Verify() NotAfter = %s, want %s (the cached roster's own expiry bounds the grace)",
					grant.NotAfter.UTC().Format(time.RFC3339), expiry.UTC().Format(time.RFC3339))
			}
		})
	}
}

// plantRequest is everything planting one bundle needs.
//
// A struct rather than six parameters: the five fields below are ONE
// description of a bundle to write, and threading them through a signature
// made the helper read as a list of unrelated arguments.
type plantRequest struct {
	// cacheDir is where the offline fallback will look.
	cacheDir string
	// plant names the shape to write: "", "expired" or "forged".
	plant string
	// vendorPriv signs a genuine bundle.
	vendorPriv ed25519.PrivateKey
	// subjects is what the planted roster lists.
	subjects map[string]coreent.SubjectValue
	// now anchors the planted window.
	now time.Time
}

// plantCase writes the bundle a row asked for, if any.
//
// Split out so the table above reads as cases rather than as a switch: the
// three planted shapes differ only in who signed them and when.
func plantCase(t *testing.T, req plantRequest) {
	t.Helper()

	//: Dispatch based on the variant to apply the correct logic.
	switch req.plant {
	//: Nothing planted: either the row warms the cache legitimately, or it
	//: is the never-fetched case.
	case "":
		return
	//: A genuine, correctly signed roster whose window has already closed.
	case "expired":
		plantBundle(t, req.cacheDir, signPair(t, req.vendorPriv, coreent.RosterValue{
			IssuedAt:  req.now.Add(-2 * time.Hour),
			ExpiresAt: req.now.Add(-time.Hour),
			Subjects:  req.subjects,
		}))
	//: A perfectly well-formed roster listing this very subject, signed by
	//: somebody who is not the vendor. If the signature were not re-checked
	//: on read, this would authorise.
	case "forged":
		_, impostor, err := ed25519.GenerateKey(nil)
		//: A failure here is an environment problem, not a test outcome.
		if err != nil {
			t.Fatalf("generating impostor key: %v", err)
		}
		plantBundle(t, req.cacheDir, signPair(t, impostor, coreent.RosterValue{
			IssuedAt:  req.now.Add(-time.Minute),
			ExpiresAt: req.now.Add(2 * time.Hour),
			Subjects:  req.subjects,
		}))
	//: An unnamed shape is a test bug, not a case.
	default:
		t.Fatalf("unknown plant %q", req.plant)
	}
}

// TestOfflineFallbackKeepsEveryOtherGate pins that going offline with a copy
// in hand skips NOTHING.
//
// This is the property the whole ordering inside Verify exists to protect. The
// update floor travels in the signed roster precisely so an out-of-date binary
// cannot escape it by going offline; possession is what makes a copied grant
// worthless on a second machine; and a roster that revoked this subject refuses
// it from the cache exactly as it would from the wire — which is why the bytes
// are kept even when the match that followed them failed.
func TestOfflineFallbackKeepsEveryOtherGate(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// floor is the roster's mandatory-update version.
		floor string
		// version is what the binary declares.
		version string
		// listed controls whether the roster still lists this subject.
		listed bool
		// dropPrivate removes the private half after the cache is warm,
		// modelling a grant file copied to a machine that has no key.
		dropPrivate bool
		wantErr     error
		reason      string
	}{
		{
			name:  "a warm, listed, current, key-holding machine is authorised",
			floor: "", version: "v1.0.0", listed: true,
			reason: "the control: every row below differs from this one in exactly one way",
		},
		{
			name:  "the update floor is applied to the cached roster too",
			floor: "v2.0.0", version: "v1.0.0", listed: true,
			wantErr: coreent.ErrUpdateRequired,
			reason:  "the floor travels in the signed document so going offline cannot skip it — including going offline with a copy",
		},
		{
			name:  "a cached roster that revoked this subject refuses it",
			floor: "", version: "v1.0.0", listed: false,
			wantErr: coreent.ErrRevoked,
			reason:  "the bundle is kept even when the match it fed failed, so the next offline start reads the revocation rather than the last roster that approved",
		},
		{
			name:  "possession is still required offline",
			floor: "", version: "v1.0.0", listed: true, dropPrivate: true,
			wantErr: coreent.ErrNoLicense,
			reason:  "a cached bundle copied to a machine without the private half authorises nothing — the same bar the online path sets",
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

			dir, cacheDir := t.TempDir(), t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			subjects := map[string]coreent.SubjectValue{}
			//: An unlisted subject is how a revocation reaches a client: the
			//: roster is a snapshot, and absence IS the withdrawal.
			if tt.listed {
				subjects[sampleUUID] = coreent.SubjectValue{Fingerprint: fingerprint}
			}

			getter := &stubGetter{bundle: signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:        now.Add(-time.Minute),
				ExpiresAt:       now.Add(2 * time.Hour),
				Subjects:        subjects,
				RequiredVersion: tt.floor,
			})}
			//: Warm the cache with a binary that clears the floor and with
			//: the key in place, so the ONLY thing each row changes is what
			//: happens on the offline attempt.
			warm := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo")).
				WithCache(cacheDir).
				WithVersion("v9.9.9")
			//: A revoked subject cannot warm its own cache through a
			//: successful verification, so the write has to happen anyway —
			//: which is exactly what rosterFrom does, before the match.
			if _, warmErr := warm.Verify(now); warmErr != nil && tt.listed {
				t.Fatalf("warming verification: %v", warmErr)
			}

			//: Now the machine looks like what each row describes.
			if tt.dropPrivate {
				if rmErr := os.Remove(entitlement.PrivateKeyPath(dir, sampleUUID)); rmErr != nil {
					t.Fatalf("removing private half: %v", rmErr)
				}
			}
			getter.fail = true

			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo")).
				WithCache(cacheDir).
				WithVersion(tt.version)

			grant, err := svc.Verify(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				//: A refusal that reported coreent.ErrRosterUnreachable would mean the
				//: cache never answered and the row proved nothing.
				if errors.Is(err, coreent.ErrRosterUnreachable) {
					t.Errorf("Verify() error = %v, want the cache to have answered first (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			if !grant.Offline {
				t.Error("Verify() grant.Offline = false, want true (the answer came from the cache)")
			}
		})
	}
}

// Test_Service_WithCache pins that the setter is what ARMS the offline
// fallback, and that "" really is off.
//
// Both rows plant the SAME perfectly usable bundle where an armed cache would
// read it and take the SAME origins down, so the only thing that differs
// between authorised and refused is the argument to this one call. That is
// what keeps a svcent.Service built around an injected getter from touching a real
// filesystem: every constructor but NewService leaves this empty, and a
// fallback that quietly armed itself would make every existing test in this
// package read a directory it never declared.
func Test_Service_WithCache(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// arm decides whether the planted directory is handed to WithCache.
		arm     bool
		wantErr error
		reason  string
	}{
		{name: "a directory arms the fallback", arm: true, reason: "the value must be the one the reader composes a path from"},
		{name: "an empty string disables it", arm: false, wantErr: coreent.ErrRosterUnreachable, reason: "the test constructors must reach no filesystem at all"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			dir, cacheDir := t.TempDir(), t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			bundle := signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now.Add(-time.Minute),
				ExpiresAt: now.Add(2 * time.Hour),
				Subjects:  map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
			})
			//: Planted for BOTH rows, so the refusal below can only come from
			//: the cache being off rather than from the cache being empty.
			plantBundle(t, cacheDir, bundle)

			armed := ""
			//: The one difference between the two rows.
			if tt.arm {
				armed = cacheDir
			}
			svc := svcent.NewServiceWithOrigins(
				&stubGetter{bundle: bundle, fail: true}, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo"),
			)
			//: Chainable: the return must be the same svcent.Service, or a
			//: construction expression would silently drop the setting.
			if got := svc.WithCache(armed); got != svc {
				t.Fatalf("WithCache() returned %p, want the receiver %p (%s)", got, svc, tt.reason)
			}

			grant, err := svc.Verify(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			if !grant.Offline {
				t.Error("Verify() grant.Offline = false, want true (the answer came from the cache)")
			}
		})
	}
}

// TestDefaultCacheDir pins that the production default is a real, per-user
// location and not the working directory.
//
// "" means the fallback is DISABLED, which is the safe direction when the
// operating system cannot name a cache root: a machine that cannot say where
// its cache lives requires the network, exactly as this package did before the
// cache existed. What must never happen is a relative path, which would put a
// roster wherever the linter happened to be invoked from.
func TestDefaultCacheDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "the default is absolute or absent", reason: "a relative path would follow the caller's working directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := testProduct.DefaultCacheDir()
			//: Absence is a legitimate answer and disables the fallback.
			if got == "" {
				return
			}
			if !filepath.IsAbs(got) {
				t.Errorf("testProduct.DefaultCacheDir() = %q, want an absolute path (%s)", got, tt.reason)
			}
		})
	}
}

// TestVerifyRefusesARegressedClock pins the one thing an offline binary can
// honestly say about time.
//
// Every deadline in this scheme — the roster window, the subject's term, the
// grant's NotAfter — is compared against a clock its holder owns. Rolling that
// clock back into a captured roster's window authorises indefinitely, and no
// signature check notices, because every document involved is genuine. The
// vendor's IssuedAt is the one timestamp the holder cannot forge; a local clock
// reading earlier than the newest one this machine ever authenticated is the
// observable, and refusing on it is the whole mechanism.
//
// What it does NOT catch is a FROZEN clock, and no row here pretends otherwise:
// parked exactly on the mark, nothing local distinguishes a stopped world from
// a still one.
//
// One honest caveat about the first row: with the SAME roster still being
// served, ParseRoster already refuses it as "issued in the future", so that row
// would pass with the ratchet removed. It pins the sentinel and the shape.
// TestTheRatchetSurvivesAnOlderGenuineRoster is the discriminating one — a
// lagging origin serving a genuinely older roster is the attack shape that
// nothing but the ratchet catches, and it returns a nil error without it.
func TestVerifyRefusesARegressedClock(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// rollback is how far the clock is moved BACK for the second
		// verification, after a first one at `now` warmed the mark.
		rollback time.Duration
		wantErr  error
		reason   string
	}{
		{
			name:     "a clock rolled back past the mark is refused",
			rollback: 3 * time.Hour,
			wantErr:  coreent.ErrClockRegressed,
			reason:   "this is the attack: a genuine older roster replayed with the clock moved into its window",
		},
		{
			name:     "a clock parked exactly on the mark is not",
			rollback: 0,
			reason:   "equality is not regression, and a frozen clock is what nothing local can catch — stated by this row rather than papered over",
		},
		{
			name:     "a clock moving forward is not",
			rollback: -time.Hour,
			reason:   "time passing is the ordinary case; only backwards is evidence of anything",
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

			dir, cacheDir := t.TempDir(), t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			//: A LONG window, so the second verification below fails on the
			//: clock rather than on a roster that merely went stale.
			getter := &stubGetter{bundle: signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now,
				ExpiresAt: now.Add(20 * time.Hour),
				Subjects:  map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
			})}
			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo")).
				WithCache(cacheDir)

			//: The first verification is what records the mark. Without it
			//: there is no evidence about time and nothing to regress from.
			if _, warmErr := svc.Verify(now); warmErr != nil {
				t.Fatalf("warming verification: %v", warmErr)
			}

			_, err := svc.Verify(now.Add(-tt.rollback))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}

// TestTheRatchetSurvivesAnOlderGenuineRoster pins that the mark only ever moves
// forward THROUGH this package.
//
// An origin lagging behind another serves a genuine roster hours older than the
// cached one. Taking it would lower the newest signed instant this machine can
// prove it has seen — which is exactly the guard a rolled-back clock needs
// lowered.
func TestTheRatchetSurvivesAnOlderGenuineRoster(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "an older genuine roster does not lower the mark", reason: "the ratchet is what checkClock reads; lowering it is what an attacker wants"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			dir, cacheDir := t.TempDir(), t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			subjects := map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}}

			//: A recent roster first.
			getter := &stubGetter{bundle: signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now,
				ExpiresAt: now.Add(20 * time.Hour),
				Subjects:  subjects,
			})}
			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo")).
				WithCache(cacheDir)
			if _, warmErr := svc.Verify(now); warmErr != nil {
				t.Fatalf("warming verification: %v", warmErr)
			}

			//: Now a LAGGING origin serving a perfectly genuine, older one.
			getter.bundle = signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now.Add(-10 * time.Hour),
				ExpiresAt: now.Add(10 * time.Hour),
				Subjects:  subjects,
			})
			if _, err := svc.Verify(now); err != nil {
				t.Fatalf("Verify() with a lagging origin error = %v, want nil (%s)", err, tt.reason)
			}

			//: The mark must still be the RECENT one, so a clock rolled back
			//: between the two is still caught.
			if _, err := svc.Verify(now.Add(-time.Hour)); !errors.Is(err, coreent.ErrClockRegressed) {
				t.Errorf("Verify() error = %v, want coreent.ErrClockRegressed (%s)", err, tt.reason)
			}
		})
	}
}
