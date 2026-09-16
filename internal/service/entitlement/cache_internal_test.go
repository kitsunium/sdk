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

			err := writeCachedBundle(tt.setup(t), []byte(`{"payload":"x","sig":"y"}`))
			if err == nil {
				t.Fatalf("writeCachedBundle() error = nil, want a failure (%s)", tt.reason)
			}
			//: Every step of the write answers to ONE sentinel. rememberRoster
			//: logs whatever comes back, and a log line that cannot name what
			//: it is reporting is what this pins.
			if !errors.Is(err, CacheUnwritable) {
				t.Errorf("writeCachedBundle() error = %v, want it to carry CacheUnwritable (%s)", err, tt.reason)
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
			//: These bytes are not a bundle at all, so they prove nothing about
			//: time and there is nothing on disk to compare them against: the
			//: ratchet has no verdict to reach and the write proceeds, which is
			//: what these rows are about. Test_Service_rememberRoster covers the
			//: comparison, and Test_rememberRoster_refusesARosterOlderThanTheMark
			//: covers what it refuses.
			//:
			//: The contract on THIS path is "returns, always, without refusing".
			//: A panic, or a refusal reached over an unwritable cache, would
			//: surface as a refused licence.
			if err := (&Service{cacheDir: dir}).rememberRoster(raw); err != nil {
				t.Fatalf("rememberRoster() error = %v, want nil — a cache failure is never a refusal (%s)", err, tt.reason)
			}

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

			roster, err := (&Service{anchors: [][]byte{vendorPub}, cacheDir: dir}).cachedRoster(now)
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

	//: A roster carrying nothing but its window, which is all the ratchet reads.
	return signedBundle(t, priv, coreent.RosterValue{IssuedAt: issued, ExpiresAt: expires})
}

// signedBundle signs an arbitrary roster into the one document the fallback
// reads.
//
// Shared by every fixture that needs a bundle rather than copied into each,
// because the bundle's shape is a contract: the signature covers the payload's
// exact bytes, and a second copy of this marshalling is a second chance to
// re-serialise them.
func signedBundle(t *testing.T, priv ed25519.PrivateKey, roster coreent.RosterValue) []byte {
	t.Helper()

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

			svc := &Service{anchors: [][]byte{vendorPub}, cacheDir: dir}
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

			got := (&Service{anchors: [][]byte{vendorPub}, cacheDir: dir}).signedHighWaterMark()
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
		// wantRefused is whether the second offer must come back as a refusal
		// as well as not being installed. Not installing and not being usable
		// are two different answers, and the ratchet now gives both.
		wantRefused bool
		reason      string
	}{
		{name: "an older bundle is not kept", age: 5 * time.Hour, wantOffset: 0, wantRefused: true, reason: "the mark only moves forward through this package, and the document that would lower it is refused rather than merely dropped"},
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
			svc := &Service{anchors: [][]byte{vendorPub}, cacheDir: dir}

			first := internalBundle(t, vendorPriv, newer, newer.Add(time.Hour))
			//: The first install has nothing to supersede, so it is always
			//: accepted; a refusal here would mean the seed itself was refused.
			if err := svc.rememberRoster(first); err != nil {
				t.Fatalf("rememberRoster() seeding error = %v, want nil (%s)", err, tt.reason)
			}

			second := newer.Add(-tt.age)
			verdict := svc.rememberRoster(internalBundle(t, vendorPriv, second, second.Add(time.Hour)))
			//: The mark and the verdict are two halves of one answer: refusing to
			//: install an older document and refusing to let the caller ACT on it
			//: are what this file got wrong for as long as it only did the first.
			if (verdict != nil) != tt.wantRefused {
				t.Fatalf("rememberRoster() error = %v, want a refusal: %t (%s)", verdict, tt.wantRefused, tt.reason)
			}
			//: And the refusal names supersession rather than a closed window,
			//: which is the distinction the condition field carries.
			if tt.wantRefused && !errors.Is(verdict, coreent.ErrRosterStale) {
				t.Errorf("rememberRoster() error = %v, want coreent.ErrRosterStale (%s)", verdict, tt.reason)
			}

			want := newer.Add(tt.wantOffset)
			if got := svc.signedHighWaterMark(); !got.Equal(want) {
				t.Errorf("signedHighWaterMark() = %s, want %s (%s)", got, want, tt.reason)
			}
		})
	}
}

// Test_Service_markWhileHeld_believesOnlyARoster pins that the ratchet advances
// on a ROSTER and not merely on something the vendor signed.
//
// The ratchet reads a document ParseBundle has deliberately not judged for
// freshness, because the ordinary state of a cached bundle is expired. What it
// used to skip alongside the window was every OTHER thing ParseRoster decides
// about the document itself: authenticateBundle authenticated and returned, so a
// signed document ParseRoster would refuse to authorise on its own shape set the
// mark anyway — and the mark is what checkClock refuses a machine on.
//
// The over-wide-window row is the sharpest of the three: ParseRoster refuses
// that document for authorisation in so many words ("window wider than the
// agreed lifetime"), and the same bytes were nonetheless the one piece of
// evidence about time this machine trusted.
//
// Three of the five rows are discriminating: inverted, absent and over-wide
// windows all returned the document's IssuedAt before the shape checks existed.
// The last row does not — a document with no iat produced the zero instant then
// and produces an absent record now — and it is kept anyway, because it pins
// that absence is a VERDICT rather than a mark that happens to be zero, which
// is the distinction the payload comparison rests on.
func Test_Service_markWhileHeld_believesOnlyARoster(t *testing.T) {
	t.Parallel()

	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	tests := []struct {
		name string
		// plant returns the signing instant and the closing instant of the
		// document written into the cache. A zero return is a document that
		// carries no such field at all.
		plant func(base time.Time) (issued, expires time.Time)
		// wantPresent is whether the planted document may set the mark.
		wantPresent bool
		reason      string
	}{
		{
			name:        "a well-formed roster is the mark",
			plant:       func(base time.Time) (time.Time, time.Time) { return base, base.Add(time.Hour) },
			wantPresent: true,
			reason:      "this is the ordinary cached bundle, and the ratchet exists to read it",
		},
		{
			name:   "an inverted window proves nothing",
			plant:  func(base time.Time) (time.Time, time.Time) { return base, base.Add(-time.Hour) },
			reason: "a window that closes before it opens is not a window, so the document is not a roster",
		},
		{
			name:   "a document with no window at all proves nothing",
			plant:  func(base time.Time) (time.Time, time.Time) { return base, time.Time{} },
			reason: "ParseRoster refuses this for authorisation; believing it about time is the same document read two ways",
		},
		{
			name: "a window wider than the agreed lifetime proves nothing",
			plant: func(base time.Time) (time.Time, time.Time) {
				return base, base.Add(coreent.RosterLifetime + time.Second)
			},
			reason: "the signature proves the vendor issued these bytes, never that the vendor issued them correctly",
		},
		{
			name:   "a document with no signing instant proves nothing",
			plant:  func(base time.Time) (time.Time, time.Time) { return time.Time{}, base.Add(time.Hour) },
			reason: "absence has to be a verdict rather than a mark that happens to read zero",
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
			issued, expires := tt.plant(base)
			//: Every row is signed by the vendor the Service trusts, so nothing
			//: below can be explained by a signature failure.
			if err := writeCachedBundle(dir, internalBundle(t, vendorPriv, issued, expires)); err != nil {
				t.Fatalf("planting bundle: %v", err)
			}

			got := (&Service{anchors: [][]byte{vendorPub}, cacheDir: dir}).markWhileHeld()
			if got.present != tt.wantPresent {
				t.Fatalf("markWhileHeld().present = %t, want %t (%s)", got.present, tt.wantPresent, tt.reason)
			}
			//: An absent record must carry no instant either, or signedHighWaterMark
			//: would still hand checkClock something to refuse on.
			if !tt.wantPresent {
				if !got.issued.IsZero() {
					t.Errorf("markWhileHeld().issued = %s, want the zero instant (%s)", got.issued, tt.reason)
				}
				return
			}
			if !got.issued.Equal(issued) {
				t.Errorf("markWhileHeld().issued = %s, want %s (%s)", got.issued, issued, tt.reason)
			}
		})
	}
}

// Test_Service_checkClock_refusesBothDirectionsOfAnImplausibleMark pins that the
// ceiling is a DIFFERENT SENTENCE and never a forgiveness.
//
// A bundle the vendor signed for next year, written straight into the cache
// directory, sets the mark there — ParseBundle refuses `now.Before(IssuedAt)`, so
// no fetch can install one, but nothing stops a file being dropped in. The
// machine is then refused on a clock that is perfectly correct, and told to set
// it. Following that advice breaks a working machine.
//
// The obvious repair is to ignore a mark that far ahead, and it is a hole: rolling
// the clock back by more than markCeiling puts the LEGITIMATE mark outside the
// ceiling too, so the record the ratchet exists to consult gets discarded by
// exactly the move it exists to catch, and checkClock returns nil. Both
// directions stay refusals; only the advice differs.
//
// Which assertion does which: `errors.Is` on the far-future row is the guard
// against that simplification — it returns nil the moment somebody turns the
// ceiling into a discarded record, and it PASSED before the ceiling existed, so
// it is not the discriminating half. The `condition` comparison is: before the
// ceiling, both rows rendered the "set your clock" sentence.
func Test_Service_checkClock_refusesBothDirectionsOfAnImplausibleMark(t *testing.T) {
	t.Parallel()

	// clockCondition is the sentence a machine whose CLOCK went backwards gets.
	const clockCondition string = "the local clock reads earlier than the newest signed instant this machine has authenticated"

	// cacheCondition is the sentence a machine holding an implausible BUNDLE
	// gets, whose clock may well be right.
	const cacheCondition string = "the newest signed instant this machine holds is further ahead of the local clock than a roster's own lifetime, so the cached bundle is what cannot be right"

	tests := []struct {
		name string
		// ahead is how far the planted bundle's IssuedAt sits ahead of the
		// clock checkClock is handed.
		ahead time.Duration
		// wantCondition is the sentence the refusal must carry.
		wantCondition string
		reason        string
	}{
		{
			name:          "a clock behind the mark blames the clock",
			ahead:         time.Hour,
			wantCondition: clockCondition,
			reason:        "inside a roster lifetime a publisher can legitimately be ahead of a client, so the clock is the thing to doubt",
		},
		{
			name:          "a mark past the ceiling blames the cache",
			ahead:         markCeiling + time.Hour,
			wantCondition: cacheCondition,
			reason:        "no publisher is a day and an hour ahead of a correct clock; telling this operator to set theirs breaks a working machine",
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

			now := time.Now().Truncate(time.Second)
			issued := now.Add(tt.ahead)
			dir := t.TempDir()
			//: Planted rather than fetched, because a fetch cannot produce this:
			//: ParseBundle refuses a roster issued in the future. A window of an
			//: hour keeps the document a well-formed roster, so the shape checks
			//: in rosterMark are not what any row below is measuring.
			if err := writeCachedBundle(dir, internalBundle(t, vendorPriv, issued, issued.Add(time.Hour))); err != nil {
				t.Fatalf("planting bundle: %v", err)
			}

			err := (&Service{anchors: [][]byte{vendorPub}, cacheDir: dir}).checkClock(now)
			//: The anti-simplification guard: a ceiling that DISCARDED the mark
			//: would return nil here, which is what the rollback wants.
			if !errors.Is(err, coreent.ErrClockRegressed) {
				t.Fatalf("checkClock() error = %v, want coreent.ErrClockRegressed — an implausible mark is refused, never forgiven (%s)", err, tt.reason)
			}
			//: The discriminating half: one sentinel, two sentences, because the
			//: two have different resolving actions.
			if got := probeField(err, "condition"); got != tt.wantCondition {
				t.Errorf("checkClock() condition = %q, want %q (%s)", got, tt.wantCondition, tt.reason)
			}
		})
	}
}
