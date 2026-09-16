// Internal tests: the anchor list — the rotation it makes possible in band, and
// the two ways it must not widen anything else.
package entitlement

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// anchorFingerprint is what the local identity presents in the end-to-end rows.
// A constant rather than a literal per row: no row here is ABOUT a fingerprint
// mismatch, so none of them may differ by accident.
const anchorFingerprint string = "SHA256:anchors"

// anchorKeys mints n vendor keypairs for one row.
//
// Generated per row rather than shared across the table so a row cannot pass on
// a key another row installed, and returned as two parallel slices because every
// row needs the public halves as a LIST and the private halves individually.
func anchorKeys(t *testing.T, n int) (pubs [][]byte, privs []ed25519.PrivateKey) {
	t.Helper()

	pubs, privs = make([][]byte, n), make([]ed25519.PrivateKey, n)
	//: One keypair per slot, in order, so a row can say "the second anchor".
	for i := range n {
		pub, priv, keyErr := ed25519.GenerateKey(nil)
		//: A failure here is an environment problem, not a test outcome.
		if keyErr != nil {
			t.Fatalf("generating anchor %d: %v", i, keyErr)
		}
		pubs[i], privs[i] = pub, priv
	}
	//: The public halves a build links in, and the private halves that sign.
	return pubs, privs
}

// Test_parseBundleAnyAnchor pins what an ORDERED list of anchors changes and
// what it must leave exactly as it was.
//
// # What was missing
//
// Service held ONE anchor, so an installation whose anchor had to change had no
// path in band: a roster signed by a new key was refused before anything read
// the version floor that would have told it to upgrade, and the release carrying
// the new anchor was refused by the installation that needed it. Rotation was
// possible only out of band.
//
// # The two directions every row is here to hold apart
//
// Accepting any anchor on the list must not become accepting anything: a key
// that is NOT on the list is refused, and an EMPTY list refuses everything
// rather than authenticating everything. And the loop must move on only from a
// document this anchor cannot AUTHENTICATE — never from one it authenticated and
// the window then refused, which the "expired" row pins by ORDER: with the
// working anchor first, a loop that kept trying would come back
// coreent.ErrRosterUnsigned and tell an operator whose publisher had simply
// fallen behind that somebody is impersonating the vendor.
func Test_parseBundleAnyAnchor(t *testing.T) {
	t.Parallel()

	base := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// anchors selects, by index into the generated keys, which public
		// halves the build links in and in what order. A negative index means
		// "a malformed anchor", which a build can produce and this package
		// must survive.
		anchors []int
		// signer selects which private half signs the bundle. A negative index
		// means "a key on no list", which is the substituted-endpoint case.
		signer int
		// expired makes the roster's window closed at the instant it is read.
		expired bool
		// wantErr is the refusal the row must draw, or nil for authenticated.
		wantErr error
		// wantCondition, when set, is the condition field the refusal carries.
		wantCondition string
		reason        string
	}{
		{
			name:    "the first anchor authenticates, as it always did",
			anchors: []int{0, 1},
			signer:  0,
			reason:  "the single-key path must not change shape because a second slot exists",
		},
		{
			name:    "the second anchor authenticates, which is the whole of rotation",
			anchors: []int{0, 1},
			signer:  1,
			reason:  "publishing under a new key must reach an installation that still also accepts the old one",
		},
		{
			name:    "a malformed anchor does not stop a working one behind it",
			anchors: []int{-1, 1},
			signer:  1,
			reason:  "a build that got one slot wrong must not lose the slots it got right",
		},
		{
			name:    "a key on no list authenticates nothing",
			anchors: []int{0, 1},
			signer:  -1,
			wantErr: coreent.ErrRosterUnsigned,
			reason:  "accepting ANY listed anchor must not become accepting any key at all",
		},
		{
			name:    "an empty list authenticates nothing either",
			anchors: []int{},
			signer:  0,
			wantErr: coreent.ErrRosterUnsigned,
			wantCondition: "no trust anchor was linked into this build, " +
				"so no signature can be valid",
			reason: "a build that linked in no anchor must refuse everything, never admit everything",
		},
		{
			name:    "a window that closed keeps its own refusal",
			anchors: []int{0, 1},
			signer:  0,
			expired: true,
			wantErr: coreent.ErrRosterStale,
			reason: "the working anchor is FIRST, so a loop that tried the next one anyway would " +
				"report a stale roster as a forgery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pubs, privs := anchorKeys(t, 3)

			anchors := make([][]byte, 0, len(tt.anchors))
			//: Resolve the row's indexes into the list the build links in. A
			//: negative slot is a malformed key rather than an absent one:
			//: absence is what the empty-list row is about.
			for _, idx := range tt.anchors {
				if idx < 0 {
					anchors = append(anchors, []byte("not an ed25519 public key"))
					continue
				}
				anchors = append(anchors, pubs[idx])
			}

			//: A key on no list is the substituted-endpoint case, which is why
			//: the third generated pair is never linked in by any row.
			signer := privs[2]
			if tt.signer >= 0 {
				signer = privs[tt.signer]
			}

			issued, expires := base.Add(-time.Hour), base.Add(time.Hour)
			//: A closed window, still comfortably inside RosterLifetime so the
			//: refusal can only have come from the window and not from its width.
			if tt.expired {
				issued, expires = base.Add(-2*time.Hour), base.Add(-time.Hour)
			}
			raw := signedBundle(t, signer, coreent.RosterValue{IssuedAt: issued, ExpiresAt: expires})

			roster, err := parseBundleAnyAnchor(raw, anchors, base)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("parseBundleAnyAnchor() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				//: Two refusals in this table share a sentinel and differ only
				//: in the condition, which is the field that keeps them
				//: tellable apart for an operator entitled to the difference.
				if tt.wantCondition != "" {
					if got := probeField(err, "condition"); got != tt.wantCondition {
						t.Errorf("parseBundleAnyAnchor() condition = %q, want %q (%s)", got, tt.wantCondition, tt.reason)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBundleAnyAnchor() error = %v, want nil (%s)", err, tt.reason)
			}
			//: An authenticated roster, and the one that was signed: a loop that
			//: returned some other anchor's reading would pass on the error
			//: alone.
			if !roster.IssuedAt.Equal(issued) {
				t.Errorf("parseBundleAnyAnchor() IssuedAt = %s, want %s (%s)",
					roster.IssuedAt.UTC().Format(time.RFC3339), issued.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}

// Test_bundleMarkAnyAnchor pins that a rotation does not reset the ratchet.
//
// The mark is the cached bundle's IssuedAt, and the cached bundle was signed
// whenever it was signed — which may well be under the anchor now being retired.
// Reading it against only ONE anchor would drop the mark at exactly the moment an
// installation is least able to re-establish it, handing back the whole rollback
// distance checkClock refuses a moved clock against.
func Test_bundleMarkAnyAnchor(t *testing.T) {
	t.Parallel()

	base := time.Now().Truncate(time.Second)

	tests := []struct {
		name string
		// anchors selects which public halves are accepted, by index.
		anchors []int
		// signer selects which private half signed the cached bundle.
		signer int
		// wantPresent is whether the bytes prove anything about time.
		wantPresent bool
		reason      string
	}{
		{
			name:        "a bundle signed by the second anchor is still evidence",
			anchors:     []int{0, 1},
			signer:      1,
			wantPresent: true,
			reason:      "a rotation must not cost the rollback distance the ratchet holds",
		},
		{
			name:        "a bundle signed by the first anchor is still evidence too",
			anchors:     []int{0, 1},
			signer:      0,
			wantPresent: true,
			reason:      "the outgoing key is still the vendor's statement about when it signed",
		},
		{
			name:        "a bundle signed by a key on no list proves nothing",
			anchors:     []int{0, 1},
			signer:      2,
			wantPresent: false,
			reason:      "a mark anyone could write is not evidence, which is why the signature is required",
		},
		{
			name:        "an empty list reads no evidence at all",
			anchors:     []int{},
			signer:      0,
			wantPresent: false,
			reason:      "with no anchor there is nothing to vouch for the bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pubs, privs := anchorKeys(t, 3)
			anchors := make([][]byte, 0, len(tt.anchors))
			//: Resolve the row's indexes into the accepted list.
			for _, idx := range tt.anchors {
				anchors = append(anchors, pubs[idx])
			}

			issued := base.Add(-time.Hour)
			raw := signedBundle(t, privs[tt.signer], coreent.RosterValue{IssuedAt: issued, ExpiresAt: base.Add(time.Hour)})

			mark := bundleMarkAnyAnchor(raw, anchors)
			if mark.present != tt.wantPresent {
				t.Fatalf("bundleMarkAnyAnchor() present = %t, want %t (%s)", mark.present, tt.wantPresent, tt.reason)
			}
			//: Present is not enough: the record must carry the instant the
			//: document actually claims, or the ratchet compares the wrong number.
			if tt.wantPresent && !mark.issued.Equal(issued) {
				t.Errorf("bundleMarkAnyAnchor() issued = %s, want %s (%s)",
					mark.issued.UTC().Format(time.RFC3339), issued.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}

// Test_anchorList pins the bound and the copy.
//
// The bound is the honest half of accepting several anchors: every entry is a key
// whose compromise is accepted while it is listed, so the list must not be a
// place retired keys accumulate. It CLAMPS rather than refusing — a build mistake
// answered by refusing every verification would take a product down over a
// misconfiguration a log line names precisely — and it keeps the HEAD, which is
// the only end droppable on a list whose order is its statement of preference.
func Test_anchorList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// declared is how many anchors the build hands over.
		declared int
		// wantKept is how many survive.
		wantKept int
		reason   string
	}{
		{name: "one key is a one-element list", declared: 1, wantKept: 1, reason: "the single-key form is not a second representation"},
		{name: "a rotation pair is kept whole", declared: 2, wantKept: 2, reason: "two is what a rotation needs and must never be clamped"},
		{name: "the bound itself is kept whole", declared: maxAnchors, wantKept: maxAnchors, reason: "a bound that clamps at itself is off by one"},
		{name: "past the bound the head survives", declared: maxAnchors + 3, wantKept: maxAnchors, reason: "the list must not become where retired keys accumulate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			declared := make([][]byte, tt.declared)
			//: Each slot distinguishable, so "kept the head" is checkable and
			//: not merely "kept the right number".
			for i := range declared {
				declared[i] = []byte{byte(i)}
			}

			held := anchorList(declared)
			if len(held) != tt.wantKept {
				t.Fatalf("anchorList() kept %d anchors, want %d (%s)", len(held), tt.wantKept, tt.reason)
			}
			//: Which ones, not how many: clamping the head would keep the count
			//: and drop the current key.
			for i := range held {
				if len(held[i]) != 1 || held[i][0] != byte(i) {
					t.Fatalf("anchorList() slot %d = %v, want the %d-th declared anchor (%s)", i, held[i], i, tt.reason)
				}
			}
		})
	}
}

// Test_anchorList_isNotExtensibleByItsCaller pins that the set a running
// verifier trusts is settled when it is built.
//
// The list's whole mitigation is that its contents are a BUILD decision. A slice
// held by reference is one `append` away from being a runtime one, and an append
// that fits the caller's spare capacity writes into the array the verifier is
// reading — so the copy is the mechanism, not a tidiness.
func Test_anchorList_isNotExtensibleByItsCaller(t *testing.T) {
	t.Parallel()

	//: Spare capacity on purpose: without it append reallocates and the test
	//: would pass against a verifier holding the caller's slice verbatim.
	declared := make([][]byte, 1, 8)
	declared[0] = []byte("current")

	held := anchorList(declared)
	grown := append(declared, []byte("smuggled"))
	//: Confirm the append really happened, so the assertion below is about the
	//: copy and not about an append that silently did nothing.
	if len(grown) != 2 {
		t.Fatalf("the fixture appended nothing: the caller's slice holds %d anchors, want 2", len(grown))
	}

	if len(held) != 1 {
		t.Fatalf("anchorList() holds %d anchors after the caller appended, want 1", len(held))
	}
	//: And the entry it does hold is still the one that was declared.
	if string(held[0]) != "current" {
		t.Errorf("anchorList() slot 0 = %q, want %q", held[0], "current")
	}
}

// Test_anchorList_keysAreNotWritableByItsCaller is the sibling of the test
// above, and it is the half that was missing.
//
// slices.Clone copies the OUTER slice, which is what stops an append from
// writing into the array this verifier reads — and it is shallow, so every
// accepted public key stayed backed by memory the caller still owns. Reusing
// or mutating one of those buffers after the constructor returned would
// silently change what a RUNNING verifier trusts: it could reject rosters the
// vendor genuinely signed, or accept one signed by a key nobody configured.
//
// A trust anchor a caller can still write to is not an anchor. Both directions
// are asserted because they are different failures: the first is an outage,
// the second is the whole scheme.
func Test_anchorList_keysAreNotWritableByItsCaller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// mutate rewrites the caller's buffer after anchorList returned.
		mutate func(key []byte)
		reason string
	}{
		{
			name:   "overwriting the key in place",
			mutate: func(key []byte) { copy(key, []byte("replaced-by-the-caller-after-the-fact")) },
			reason: "this is the substitution: the verifier would authenticate against a key nobody configured",
		},
		{
			name:   "zeroing the key",
			mutate: func(key []byte) { clear(key) },
			reason: "a wiped buffer makes every genuine roster unverifiable, which is an outage with no diagnosis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: Long enough that an in-place overwrite really changes it, and
			//: planted so the assertion reads the value it was given.
			original := []byte("the-vendor-key-as-configured-at-build")
			declared := [][]byte{original}

			held := anchorList(declared)
			//: A copy taken BEFORE the mutation, so the comparison is against
			//: what was declared rather than against whatever the buffer says
			//: afterwards.
			want := bytes.Clone(original)

			tt.mutate(original)
			//: Confirm the mutation really landed, or the assertion below is
			//: about a fixture that did nothing.
			if bytes.Equal(original, want) {
				t.Fatalf("the fixture mutated nothing: the caller's buffer still reads %q", original)
			}

			if !bytes.Equal(held[0], want) {
				t.Errorf("anchorList() slot 0 = %q, want %q — the caller still owns the key bytes this verifier trusts (%s)",
					held[0], want, tt.reason)
			}
		})
	}
}

// Test_Verify_acceptsARosterSignedByAnyAcceptedAnchor is the audit's F32
// property end to end: an installation carrying {A, B} is served by either.
//
// Before the list, `Service.vendor` was one key and the row signed by B came back
// coreent.ErrRosterUnsigned — the spoofing sentinel — from a roster the vendor
// had genuinely signed. That is the state the constat describes as blocked from
// both sides: the roster is refused before the version floor is read, and the
// release that would carry the new anchor is refused by the anchor it replaces.
func Test_Verify_acceptsARosterSignedByAnyAcceptedAnchor(t *testing.T) {
	tests := []struct {
		name string
		// signer selects which private half signs the served roster: 0 and 1
		// are linked in, 2 is on no list.
		signer int
		// wantErr is the refusal, or nil when the machine must be entitled.
		wantErr error
		reason  string
	}{
		{name: "the outgoing anchor still authorises", signer: 0, reason: "an installation must not be broken by the arrival of a second anchor"},
		{name: "the incoming anchor authorises", signer: 1, reason: "this is the rotation, and without it there is no path in band"},
		{name: "a key on neither list authorises nothing", signer: 2, wantErr: coreent.ErrRosterUnsigned, reason: "the list must widen rotation, never trust"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Both variables cleared, so the ambient environment cannot send a
			//: row down the CI path. t.Setenv is why this test is sequential.
			t.Setenv(actionsTokenURLEnv, "")
			t.Setenv(actionsTokenBearerEnv, "")

			pubs, privs := anchorKeys(t, 3)
			base := time.Now().Truncate(time.Second)
			roster := coreent.RosterValue{
				IssuedAt:  base.Add(-time.Hour),
				ExpiresAt: base.Add(time.Hour),
				Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: anchorFingerprint}},
			}

			svc := NewServiceWithGetter(
				stubRoundTripper{bundle: signedBundle(t, privs[tt.signer], roster)},
				stubIdentity{subject: sampleSubject, fingerprint: anchorFingerprint},
				nil, &testProduct).
				WithAnchors([][]byte{pubs[0], pubs[1]}).
				WithCache(t.TempDir())

			grant, err := svc.Verify(base)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			//: Entitled, and against the document that was actually served.
			if grant.Subject != sampleSubject {
				t.Errorf("Verify() grant.Subject = %q, want %q (%s)", grant.Subject, sampleSubject, tt.reason)
			}
		})
	}
}

// Test_Verify_theRatchetReadsAMarkSignedByAnySecondAnchor pins that the
// anti-rollback distance survives a rotation.
//
// The mark is the cached bundle and nothing else, so a machine whose cache was
// written under the incoming anchor must still be refused when its clock is
// rolled back behind it. Reading the mark against one anchor would silently
// forgive that rollback on every machine mid-rotation — the moment the
// installation can least afford to lose it.
func Test_Verify_theRatchetReadsAMarkSignedByAnySecondAnchor(t *testing.T) {
	//: Both variables cleared, so the ambient environment cannot decide.
	t.Setenv(actionsTokenURLEnv, "")
	t.Setenv(actionsTokenBearerEnv, "")

	pubs, privs := anchorKeys(t, 2)
	base := time.Now().Truncate(time.Second)
	roster := coreent.RosterValue{
		IssuedAt:  base,
		ExpiresAt: base.Add(10 * time.Hour),
		Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: anchorFingerprint}},
	}

	//: Signed by the SECOND anchor: the cached document is what the ratchet
	//: reads, and the point is that it was signed under the incoming key.
	svc := NewServiceWithGetter(
		stubRoundTripper{bundle: signedBundle(t, privs[1], roster)},
		stubIdentity{subject: sampleSubject, fingerprint: anchorFingerprint},
		nil, &testProduct).
		WithAnchors([][]byte{pubs[0], pubs[1]}).
		WithCache(t.TempDir())

	//: Warm the cache: this is what installs the mark.
	if _, warmErr := svc.Verify(base); warmErr != nil {
		t.Fatalf("warming Verify() error = %v, want nil — the premise does not hold", warmErr)
	}
	//: Without this the rest could pass against a cache that was never written,
	//: which is the shape that guarantees its own result.
	if mark := svc.signedHighWaterMark(); !mark.Equal(roster.IssuedAt) {
		t.Fatalf("the mark reads %s after caching a roster issued %s — a bundle signed by an accepted anchor must set it",
			mark.UTC().Format(time.RFC3339), roster.IssuedAt.UTC().Format(time.RFC3339))
	}

	//: A fresh Service over the SAME directory reads the mark off the disk and
	//: nothing else, which is what a restarted process does.
	fresh := NewServiceWithGetter(
		stubRoundTripper{bundle: signedBundle(t, privs[1], roster)},
		stubIdentity{subject: sampleSubject, fingerprint: anchorFingerprint},
		nil, &testProduct).
		WithAnchors([][]byte{pubs[0], pubs[1]}).
		WithCache(svc.cacheDir)

	//: A clock a day behind the newest signed instant this machine holds. The
	//: refusal can only come from the mark, and the mark can only be read
	//: through the second anchor.
	_, err := fresh.Verify(base.Add(-24 * time.Hour))
	if !errors.Is(err, coreent.ErrClockRegressed) {
		t.Fatalf("Verify() on a rolled-back clock error = %v, want %v — the ratchet lost a mark signed by an accepted anchor",
			err, coreent.ErrClockRegressed)
	}
}
