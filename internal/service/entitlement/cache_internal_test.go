package entitlement

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Test_writeCachedBundle pins that every failure on
// the write path is REPORTED rather than swallowed into a half-written file.
//
// The caller treats a write failure as best-effort — a cache it cannot write
// costs the offline fallback and nothing else — but "best-effort" has to mean
// the caller decided that, not that the writer lost the error on the way out.
// A truncated bundle is an unparseable one, which would destroy the very grace
// window a later outage depends on.
func Test_writeCachedBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// setup returns the directory to write into, after arranging whatever
		// makes the write fail.
		setup  func(t *testing.T) string
		reason string
	}{
		{
			name: "a cache root that cannot be created",
			setup: func(t *testing.T) string {
				t.Helper()
				//: A regular file where a directory component must be: every
				//: platform refuses to create a child of it.
				blocked := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
					t.Fatalf("writing blocker: %v", err)
				}
				return filepath.Join(blocked, "cache")
			},
			reason: "MkdirAll is the first thing that can fail on a fresh machine",
		},
		{
			name: "a destination that is already a directory",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				//: A directory wearing the bundle's name: staging succeeds and
				//: the rename is what refuses.
				if err := os.MkdirAll(cachedBundlePath(dir), 0o700); err != nil {
					t.Fatalf("creating blocker: %v", err)
				}
				return dir
			},
			reason: "the rename is the step that makes the replacement atomic, so its failure must not look like success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := writeCachedBundle(tt.setup(t), []byte(`{"payload":"x","sig":"y"}`)); err == nil {
				t.Errorf("writeCachedBundle() error = nil, want a failure (%s)", tt.reason)
			}
		})
	}
}

// Test_removeBestEffort pins both halves
// of the staging cleanup.
//
// It runs on the failure path of every write, so an error or a panic here
// would replace a reportable write failure with something far less useful —
// and a staging file it failed to drop would accumulate one per failed write.
func Test_removeBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// create decides whether the target exists before the call.
		create bool
		reason string
	}{
		{name: "an existing staging file is dropped", create: true, reason: "one leftover per failed write would accumulate silently"},
		{name: "a path that was never created is tolerated", create: false, reason: "cleanup runs after failures, and must not add one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "staged.tmp")
			//: Only the first row has anything to remove.
			if tt.create {
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatalf("writing staging file: %v", err)
				}
			}

			removeBestEffort(path)

			//: Gone either way is the whole contract, whichever way it got
			//: there.
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("os.Stat(%s) error = %v, want fs.ErrNotExist (%s)", path, err, tt.reason)
			}
		})
	}
}

// Test_rememberRoster_keepsWhatItCanAndSurvivesWhatItCannot pins both sides of
// the one trade-off this file makes.
//
// A cache it CAN write is kept, because that file is the whole offline
// fallback. A cache it cannot write must never turn a successful verification
// into a refusal: the fallback is a convenience, and a machine with a
// read-only cache root has to keep running online exactly as it did before any
// cache existed.
func Test_rememberRoster_keepsWhatItCanAndSurvivesWhatItCannot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// dir returns where the Service is told to keep its cache.
		dir func(t *testing.T) string
		// wantKept is whether the bundle must be readable back afterwards.
		wantKept bool
		reason   string
	}{
		{
			name:     "a writable cache keeps the bytes verbatim",
			dir:      func(t *testing.T) string { t.Helper(); return t.TempDir() },
			wantKept: true,
			reason:   "the signature covers these exact bytes, so anything but verbatim is unverifiable",
		},
		{
			name:     "no cache configured writes nothing",
			dir:      func(*testing.T) string { return "" },
			wantKept: false,
			reason:   "every test constructor leaves it unset and must touch no filesystem",
		},
		{
			name: "an unwritable cache root is survived",
			dir: func(t *testing.T) string {
				t.Helper()
				blocked := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
					t.Fatalf("writing blocker: %v", err)
				}
				return filepath.Join(blocked, "cache")
			},
			wantKept: false,
			reason:   "a read-only home costs the grace window, never the verification",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"payload":"x","sig":"y"}`)
			dir := tt.dir(t)
			//: A nil roster carries no IssuedAt, so the ratchet has nothing to
			//: compare and the write proceeds — which is what these rows are
			//: about. Test_rememberRoster_neverLowersTheMark covers the other
			//: half.
			//:
			//: The contract is "returns, always". A panic or a propagated
			//: error here would surface as a refused licence.
			(&Service{cacheDir: dir}).rememberRoster(raw, nil)

			//: Nothing to read back when the cache is off or unwritable; the
			//: absence IS the assertion there.
			if !tt.wantKept {
				return
			}
			back, readErr := os.ReadFile(cachedBundlePath(dir))
			if readErr != nil {
				t.Fatalf("reading back the cached bundle: %v (%s)", readErr, tt.reason)
			}
			if string(back) != string(raw) {
				t.Errorf("cached bundle = %q, want %q (%s)", back, raw, tt.reason)
			}
		})
	}
}

// Test_cachedBundlePath pins the file the fallback reads and writes.
//
// The name is part of the contract with every machine already in the field: a
// rename silently costs all of them their grace window on the next upgrade,
// and nothing else would notice.
func Test_cachedBundlePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		dir    string
		want   string
		reason string
	}{
		{name: "the published name, under the cache root", dir: filepath.Join("a", "b"), want: filepath.Join("a", "b", "roster.signed.json"), reason: "it carries the published name so an operator can tell what the file is"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cachedBundlePath(tt.dir); got != tt.want {
				t.Errorf("cachedBundlePath(%q) = %q, want %q (%s)", tt.dir, got, tt.want, tt.reason)
			}
		})
	}
}

// Test_Service_cachedRoster pins that bytes off the disk go through the SAME
// door as bytes off the wire.
//
// The cache is read from a location its holder controls, so it is exactly as
// untrusted as an origin's response: ParseBundle checks the vendor signature
// before decoding anything, and ParseRoster refuses a window that has closed.
// Nothing on this path is more permissive than the network path — it is the
// same document, read from somewhere else.
func Test_Service_cachedRoster(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// arm decides whether a cache directory is configured at all.
		arm bool
		// bundle is what sits in the cache, or nil for an empty one.
		bundle func(t *testing.T, vendor ed25519.PrivateKey) []byte
		// impostor signs the planted bundle with somebody else's key.
		impostor bool
		wantErr  error
		reason   string
	}{
		{
			name: "a genuine, in-window bundle authenticates",
			arm:  true,
			bundle: func(t *testing.T, vendor ed25519.PrivateKey) []byte {
				t.Helper()
				return internalBundle(t, vendor, now.Add(-time.Minute), now.Add(time.Hour))
			},
			reason: "this is the fallback working",
		},
		{
			name:    "no cache configured answers nothing",
			arm:     false,
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "composing a path from an empty directory would read whatever sat at roster.signed.json in the working directory",
		},
		{
			name:    "an empty cache answers nothing",
			arm:     true,
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "a machine that never fetched one still cannot start",
		},
		{
			name: "a bundle past its window is refused",
			arm:  true,
			bundle: func(t *testing.T, vendor ed25519.PrivateKey) []byte {
				t.Helper()
				return internalBundle(t, vendor, now.Add(-2*time.Hour), now.Add(-time.Hour))
			},
			wantErr: coreent.ErrRosterStale,
			reason:  "a frozen FILE stops authorising at its own ExpiresAt — this is the half of the no-cache objection that is answered",
		},
		{
			//: The discriminating case, and it has to be a GENUINE bundle to
			//: discriminate at all: a blob of junk past the cap is refused
			//: either way — by the cap, or by the JSON parser after being read
			//: whole — so it would pass with or without the fix. This one is
			//: correctly signed and inside its window, so without the cap it
			//: AUTHORISES. Only the cap refuses it.
			name: "a genuine bundle past the artefact cap is refused before it is read whole",
			arm:  true,
			bundle: func(t *testing.T, vendor ed25519.PrivateKey) []byte {
				t.Helper()
				return oversizedBundle(t, vendor, now)
			},
			wantErr: coreent.ErrRosterUnreachable,
			reason:  "the cache is untrusted input read off a disk its holder controls, so it gets the untrusted-input cap — not a different one, the same one",
		},
		{
			name: "a bundle signed by anyone else is refused",
			arm:  true,
			bundle: func(t *testing.T, vendor ed25519.PrivateKey) []byte {
				t.Helper()
				return internalBundle(t, vendor, now.Add(-time.Minute), now.Add(time.Hour))
			},
			impostor: true,
			wantErr:  coreent.ErrRosterUnsigned,
			reason:   "the signature is what makes these bytes a roster rather than a file",
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
			signWith := vendorPriv
			//: The impostor row signs with a key the verifier never saw.
			if tt.impostor {
				_, other, otherErr := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem, not a test outcome.
				if otherErr != nil {
					t.Fatalf("generating impostor key: %v", otherErr)
				}
				signWith = other
			}

			dir := ""
			//: An armed row gets a real directory; a disarmed one gets "".
			if tt.arm {
				dir = t.TempDir()
			}
			//: Plant only when the row describes something to plant.
			if tt.bundle != nil {
				if err := writeCachedBundle(dir, tt.bundle(t, signWith)); err != nil {
					t.Fatalf("planting bundle: %v", err)
				}
			}

			roster, err := (&Service{vendor: vendorPub, cacheDir: dir}).cachedRoster(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("cachedRoster() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("cachedRoster() error = %v, want nil (%s)", err, tt.reason)
			}
			if roster == nil {
				t.Errorf("cachedRoster() roster = nil, want the planted one (%s)", tt.reason)
			}
		})
	}
}

// internalBundle signs a minimal roster over the window given.
//
// Local to this file rather than shared with the external tests: those build
// through the exported API on purpose, and an internal test that borrowed
// their helper would stop being a test of the unexported path.
func internalBundle(t *testing.T, priv ed25519.PrivateKey, issued, expires time.Time) []byte {
	t.Helper()

	raw, marshalErr := json.Marshal(coreent.RosterValue{IssuedAt: issued, ExpiresAt: expires})
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

// oversizedBundle signs a perfectly valid roster that is larger than the
// artefact cap.
//
// It pads ONE subject's fingerprint rather than listing thousands, which keeps
// the fixture cheap and makes what is being tested unmistakable: the size, and
// nothing else. Everything about this document is correct — the signature
// verifies, the window is open — so any refusal it draws can only have come
// from the bound.
func oversizedBundle(t *testing.T, priv ed25519.PrivateKey, now time.Time) []byte {
	t.Helper()

	raw, marshalErr := json.Marshal(coreent.RosterValue{
		IssuedAt:  now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
		Subjects: map[string]coreent.SubjectValue{
			sampleSubject: {Fingerprint: strings.Repeat("A", int(maxArtefactBytes)+1)},
		},
	})
	//: A failure here is an environment problem, not a test outcome.
	if marshalErr != nil {
		t.Fatalf("marshalling oversized roster: %v", marshalErr)
	}
	bundle, bundleErr := json.Marshal(BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if bundleErr != nil {
		t.Fatalf("marshalling oversized bundle: %v", bundleErr)
	}
	//: A genuine bundle whose only defect is its size.
	return bundle
}

// Test_readCappedFile pins that a file on disk is read under the SAME bound an
// endpoint's response is.
//
// Tested on its own, not only through cachedRoster, because the bound is the
// only thing standing between this package and a memory-exhaustion lever and
// the cap belongs to the READ rather than to any particular caller: a second
// caller added later gets the property by using this function, and a test named
// after it is what says so.
func Test_readCappedFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// size is how many bytes sit at the path, or -1 for no file at all.
		size int64
		// wantErr is whether the read must be refused.
		wantErr bool
		reason  string
	}{
		{name: "an absent file is refused", size: -1, wantErr: true, reason: "a machine that never fetched one still cannot start"},
		{name: "an ordinary file is read whole", size: 2048, reason: "a roster of thousands of subjects fits far inside the cap"},
		{name: "exactly the cap is still an artefact", size: maxArtefactBytes, reason: "the bound is inclusive"},
		{name: "one byte past the cap is refused", size: maxArtefactBytes + 1, wantErr: true, reason: "the holder chooses the size, so we choose the ceiling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), cachedBundleName)
			//: A negative size means the row is about the absent file.
			if tt.size >= 0 {
				if err := os.WriteFile(path, make([]byte, tt.size), 0o600); err != nil {
					t.Fatalf("writing fixture: %v", err)
				}
			}

			got, err := readCappedFile(path)
			if tt.wantErr {
				//: Refused as an unusable artefact, never as a decision.
				if !errors.Is(err, coreent.ErrRosterUnreachable) {
					t.Errorf("readCappedFile() error = %v, want coreent.ErrRosterUnreachable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("readCappedFile() error = %v, want nil (%s)", err, tt.reason)
			}
			if int64(len(got)) != tt.size {
				t.Errorf("readCappedFile() read %d bytes, want %d (%s)", len(got), tt.size, tt.reason)
			}
		})
	}
}

// Test_Service_checkClock pins the ratchet's decision in isolation: only
// BACKWARDS is evidence of anything.
//
// Equality is deliberately not a refusal. A clock parked exactly on the mark is
// a frozen one, and a frozen clock is what no purely local scheme can detect —
// refusing on equality would refuse the ordinary case of two verifications in
// the same second without catching the thing it was aimed at.
func Test_Service_checkClock(t *testing.T) {
	t.Parallel()

	mark := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// armed is whether a signed bundle is planted to act as the mark.
		armed bool
		// offset is applied to the mark to produce the clock reading.
		offset  time.Duration
		wantErr bool
		reason  string
	}{
		{name: "no evidence refuses nothing", armed: false, offset: -time.Hour, reason: "a machine that never fetched a roster has no statement about time, which is a fact rather than a fault"},
		{name: "a clock behind the mark is refused", armed: true, offset: -time.Second, wantErr: true, reason: "backwards past signed evidence is the one thing an offline binary can honestly assert"},
		{name: "a clock on the mark is not", armed: true, offset: 0, reason: "equality is a frozen clock, which nothing local catches; refusing it would cost the ordinary case for nothing"},
		{name: "a clock ahead of the mark is not", armed: true, offset: time.Hour, reason: "time passing is the ordinary case"},
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
			//: Only the armed rows have a mark to regress from.
			if tt.armed {
				//: A bundle whose window has already CLOSED, which is the
				//: ordinary state of a cached one — the ratchet must read its
				//: IssuedAt anyway, or it would forget the moment it mattered.
				planted := internalBundle(t, vendorPriv, mark, mark.Add(time.Minute))
				if err := writeCachedBundle(dir, planted); err != nil {
					t.Fatalf("planting bundle: %v", err)
				}
			}

			svc := &Service{vendor: vendorPub, cacheDir: dir}
			err := svc.checkClock(mark.Add(tt.offset))
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrClockRegressed) {
					t.Errorf("checkClock() error = %v, want coreent.ErrClockRegressed (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("checkClock() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}

// Test_Service_signedHighWaterMark pins that the mark comes from AUTHENTICATED
// bytes and from nothing else.
//
// A mark taken on trust would let anyone who can write the cache file pin this
// machine's clock wherever they liked — a denial of service handed over for
// free, and through the very file the ratchet exists to defend.
func Test_Service_signedHighWaterMark(t *testing.T) {
	t.Parallel()

	issued := time.Now().Add(-time.Hour).Truncate(time.Second)

	tests := []struct {
		name string
		// plant is what sits in the cache: "" for nothing, "genuine" or
		// "forged".
		plant string
		// wantMark is whether a non-zero mark must come back.
		wantMark bool
		reason   string
	}{
		{name: "no cache yields no mark", plant: "", reason: "absence of evidence is not a fault"},
		{name: "a genuine bundle yields its IssuedAt", plant: "genuine", wantMark: true, reason: "the vendor's signature is what makes this instant unforgeable"},
		{name: "a bundle signed by anyone else yields nothing", plant: "forged", reason: "an unsigned number would let a local attacker pin the clock through the ratchet itself"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}
			signWith := vendorPriv
			//: The forged row signs with a key the verifier never saw.
			if tt.plant == "forged" {
				_, other, otherErr := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem, not a test outcome.
				if otherErr != nil {
					t.Fatalf("generating impostor key: %v", otherErr)
				}
				signWith = other
			}

			dir := t.TempDir()
			//: Plant only when the row describes something to plant.
			if tt.plant != "" {
				//: An already-closed window, the ordinary state of a cache.
				if err := writeCachedBundle(dir, internalBundle(t, signWith, issued, issued.Add(time.Minute))); err != nil {
					t.Fatalf("planting bundle: %v", err)
				}
			}

			got := (&Service{vendor: vendorPub, cacheDir: dir}).signedHighWaterMark()
			//: Zero means "nothing recorded", which is a complete answer.
			if !tt.wantMark {
				if !got.IsZero() {
					t.Errorf("signedHighWaterMark() = %s, want the zero time (%s)", got, tt.reason)
				}
				return
			}
			if !got.Equal(issued) {
				t.Errorf("signedHighWaterMark() = %s, want %s (%s)", got, issued, tt.reason)
			}
		})
	}
}

// Test_Service_rememberRoster pins the ratchet on the write side.
//
// An origin lagging behind another serves a genuine roster hours older than the
// cached one. Taking it would lower the newest signed instant this machine can
// prove it has seen — which is precisely the guard a rolled-back clock needs
// lowered, arriving through a perfectly honest code path.
func Test_Service_rememberRoster(t *testing.T) {
	t.Parallel()

	newer := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// age is how much older the second bundle is.
		age time.Duration
		// wantMark is the mark expected afterwards, relative to newer.
		wantOffset time.Duration
		reason     string
	}{
		{name: "an older bundle is not kept", age: 5 * time.Hour, wantOffset: 0, reason: "the mark only moves forward through this package"},
		{name: "a newer bundle replaces it", age: -5 * time.Hour, wantOffset: 5 * time.Hour, reason: "fresher evidence is the point of fetching at all"},
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
			svc := &Service{vendor: vendorPub, cacheDir: dir}

			first := internalBundle(t, vendorPriv, newer, newer.Add(time.Hour))
			svc.rememberRoster(first, &coreent.RosterValue{IssuedAt: newer})

			second := newer.Add(-tt.age)
			svc.rememberRoster(
				internalBundle(t, vendorPriv, second, second.Add(time.Hour)),
				&coreent.RosterValue{IssuedAt: second},
			)

			want := newer.Add(tt.wantOffset)
			if got := svc.signedHighWaterMark(); !got.Equal(want) {
				t.Errorf("signedHighWaterMark() = %s, want %s (%s)", got, want, tt.reason)
			}
		})
	}
}
