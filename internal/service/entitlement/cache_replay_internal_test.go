// Internal tests: the anti-replay half of the ratchet, exercised through Verify.
package entitlement

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// replayFingerprint is what the local identity presents in every row below. A
// constant rather than a literal per row: a fingerprint that differs between the
// roster and the identity is what one row is ABOUT, so the other rows must not
// differ by accident.
const replayFingerprint string = "SHA256:replay"

// Test_Verify_anOlderGenuineRosterCannotUndoADecisionAlreadyReached is the
// audit's central property, stated as the four things a replay used to buy.
//
// # What was broken
//
// The ratchet guarded STORAGE and not ACCEPTANCE. rememberRoster refused to cache
// a roster older than the mark; rosterFrom handed that same roster to the decision
// anyway, and Verify never compared the roster in its hand against the mark. So a
// genuine roster signed before a revocation — served by a lagging mirror, a
// substituted origin, or an attacker who kept a copy — was denied a place in the
// cache and granted a seat at the table. Everything the newer roster decided was
// rollable back: the subject's entry, its fingerprint, the mandatory-update floor,
// the CI seat, and the CI POLICY itself.
//
// # Shape of every row
//
// Two rosters the vendor signed, A older than B, both windows open at `now`. B is
// fetched and kept first — and the warm-up Verify is EXPECTED to fail in most
// rows, which is the point: rememberRoster runs before the version floor and
// before the subject match precisely so a roster that revokes this machine still
// advances the mark. Then A is served in B's place and the same Verify runs again.
// A must change nothing.
//
// Every row is discriminating. Before the fix, A was accepted and the four rows
// asserting a refusal returned a grant instead; the two rows asserting a grant
// distinguish "accepted from the origin" from "answered by the cache" through
// grant.Offline, and the second of those returned coreent.ErrRevoked.
//
// # What this does NOT claim
//
// The guarantee is conditional on the mark persisting — see rememberRoster for
// the three ways it lapses. Every row here has a writable cache and takes the
// guard uncontended, which is the case the guarantee covers.
func Test_Verify_anOlderGenuineRosterCannotUndoADecisionAlreadyReached(t *testing.T) {
	tests := []struct {
		name string
		// version is what the binary declares, for the row about the floor.
		version string
		// inCI controls whether both runner variables are present.
		inCI bool
		// newer returns B: the roster fetched and kept first.
		newer func(base time.Time) coreent.RosterValue
		// older returns A: the genuine roster replayed in B's place.
		older func(base time.Time) coreent.RosterValue
		// wantErr is what the replay must still refuse with, or nil when it
		// must still authorise.
		wantErr error
		// wantCondition, when set, is the condition field the refusal carries.
		wantCondition string
		// wantOffline is the grant's Offline flag, read only when wantErr is
		// nil. It is what separates "the origin's answer was taken" from "the
		// origin's answer was refused and the cache answered".
		wantOffline bool
		reason      string
	}{
		{
			name: "a roster from before the revocation does not un-revoke",
			newer: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{IssuedAt: base, ExpiresAt: base.Add(10 * time.Hour)}
			},
			older: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{
					IssuedAt:  base.Add(-2 * time.Hour),
					ExpiresAt: base.Add(8 * time.Hour),
					Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: replayFingerprint}},
				}
			},
			wantErr: coreent.ErrRevoked,
			reason:  "a revocation that any kept copy of yesterday's roster undoes is not a revocation",
		},
		{
			name: "a roster from before the rotation does not restore the old fingerprint",
			newer: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{
					IssuedAt:  base,
					ExpiresAt: base.Add(10 * time.Hour),
					Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: "SHA256:rotated"}},
				}
			},
			older: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{
					IssuedAt:  base.Add(-2 * time.Hour),
					ExpiresAt: base.Add(8 * time.Hour),
					Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: replayFingerprint}},
				}
			},
			wantErr: coreent.ErrKeyMismatch,
			reason:  "rotating a compromised key is worth nothing if the superseded fingerprint can be re-published by a replay",
		},
		{
			name:    "a roster from before the floor was raised does not lower it",
			version: "v1.0.0",
			newer: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{
					IssuedAt:        base,
					ExpiresAt:       base.Add(10 * time.Hour),
					Subjects:        map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: replayFingerprint}},
					RequiredVersion: "v2.0.0",
				}
			},
			older: func(base time.Time) coreent.RosterValue {
				return coreent.RosterValue{
					IssuedAt:        base.Add(-2 * time.Hour),
					ExpiresAt:       base.Add(8 * time.Hour),
					Subjects:        map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: replayFingerprint}},
					RequiredVersion: "v1.0.0",
				}
			},
			wantErr: coreent.ErrUpdateRequired,
			reason:  "the floor lives in the signed roster so it cannot be skipped by going offline; a replay is the same skip by another route",
		},
		{
			name:  "a roster from before the CI account was withdrawn does not restore its seat",
			inCI:  true,
			newer: func(base time.Time) coreent.RosterValue { return replaySeatedRoster(base, nil, false) },
			older: func(base time.Time) coreent.RosterValue {
				return replaySeatedRoster(base.Add(-2*time.Hour), map[string]coreent.CIEntitlementValue{"42": {}}, false)
			},
			wantErr: coreent.ErrCINotEntitled,
			//: The condition is the assertion: this exact sentence can only come
			//: from a roster with no `ci` block at all, which is B's. A roster
			//: carrying a block sends ciSeat to the key set instead, and refuses
			//: with a different sentinel entirely.
			wantCondition: "the roster carries no CI block at all",
			reason:        "withdrawing an account's CI entitlement must not be undone by any copy of the roster that granted it",
		},
		{
			name:  "a roster from before CI was made strict does not relax it again",
			inCI:  true,
			newer: func(base time.Time) coreent.RosterValue { return replaySeatedRoster(base, nil, false) },
			older: func(base time.Time) coreent.RosterValue {
				return replaySeatedRoster(base.Add(-2*time.Hour), nil, true)
			},
			wantErr:       coreent.ErrCINotEntitled,
			wantCondition: "the roster carries no CI block at all",
			//: The sharpest row. CIRelaxed can only RELAX, so absence is the
			//: strict reading and the signature is what stops a downgrade being
			//: forged — but a replay needs no forgery. Accepting A restores the
			//: fall-through, the device key on the runner answers, and the hole
			//: ciRefusalIsFinal closed is open again through an honest document.
			reason: "the POLICY is replayable too, which is the half a signature cannot defend on its own",
		},
		{
			name: "the same roster fetched again is not a replay",
			newer: func(base time.Time) coreent.RosterValue {
				return replaySeatedRoster(base, nil, false)
			},
			older: func(base time.Time) coreent.RosterValue {
				return replaySeatedRoster(base, nil, false)
			},
			//: The guard against over-correcting. Refusing an equal IssuedAt
			//: outright would refuse every ordinary re-verification inside one
			//: publication interval, which is most of them. Offline false is what
			//: says the ORIGIN's answer was taken rather than the cache's.
			reason: "equality is the ordinary case; only a DIFFERENT statement at the same instant is a replay",
		},
		{
			name: "a different roster at the same instant is a replay",
			newer: func(base time.Time) coreent.RosterValue {
				return replaySeatedRoster(base, nil, false)
			},
			older: func(base time.Time) coreent.RosterValue {
				//: Same instant, different payload: the subject is gone.
				return coreent.RosterValue{IssuedAt: base, ExpiresAt: base.Add(10 * time.Hour)}
			},
			//: The origin's answer is refused and the cache's is what authorises,
			//: so this row asserts a GRANT that is offline. Before the fix the
			//: second document was taken and this was coreent.ErrRevoked.
			wantOffline: true,
			reason:      "the digest is over the signed payload, so two statements at one instant are two statements",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}

			url, bearer := "", ""
			//: Both variables, or neither: InCI requires the pair. Set on every
			//: row so the ambient environment cannot decide a case.
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "runner-secret"
			}
			t.Setenv(actionsTokenURLEnv, url)
			t.Setenv(actionsTokenBearerEnv, bearer)

			base := time.Now().Truncate(time.Second)
			newer, older := tt.newer(base), tt.older(base)
			getter := &stubRoundTripper{bundle: signedBundle(t, vendorPriv, newer)}
			svc := NewServiceWithGetter(getter,
				stubIdentity{subject: sampleSubject, fingerprint: replayFingerprint},
				vendorPub, &testProduct).
				WithCache(t.TempDir()).
				WithVersion(tt.version)

			//: The warm-up. Most rows expect it to REFUSE, and that is the
			//: property being set up rather than an accident: rememberRoster runs
			//: before the version floor and before the subject match, so a roster
			//: that refuses this machine still advances the mark.
			_, warmErr := svc.Verify(base)
			if !errors.Is(warmErr, tt.wantErr) {
				t.Fatalf("warming Verify() error = %v, want %v — the row's premise does not hold (%s)", warmErr, tt.wantErr, tt.reason)
			}
			//: Without this the rest of the test could pass on a cache that was
			//: never written, which is the shape that guarantees its own result.
			if mark := svc.signedHighWaterMark(); !mark.Equal(newer.IssuedAt) {
				t.Fatalf("the mark reads %s after keeping a roster issued %s — a refusing roster must still advance it",
					mark.UTC().Format(time.RFC3339), newer.IssuedAt.UTC().Format(time.RFC3339))
			}

			//: Now the replay, from the only origin there is.
			getter.bundle = signedBundle(t, vendorPriv, older)
			grant, err := svc.Verify(base)
			//: The mark is never lowered by the attempt, whatever else happens.
			if mark := svc.signedHighWaterMark(); !mark.Equal(newer.IssuedAt) {
				t.Errorf("the mark reads %s after the replay, want %s (%s)",
					mark.UTC().Format(time.RFC3339), newer.IssuedAt.UTC().Format(time.RFC3339), tt.reason)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() after the replay error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				//: Only some rows can say WHICH roster answered from the
				//: sentinel alone; those that cannot say it from the condition.
				if tt.wantCondition != "" {
					if got := probeField(err, "condition"); got != tt.wantCondition {
						t.Errorf("Verify() condition = %q, want %q (%s)", got, tt.wantCondition, tt.reason)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() after the replay error = %v, want nil (%s)", err, tt.reason)
			}
			//: Offline is what distinguishes "the origin's answer was taken"
			//: from "the origin's answer was refused and the cache answered",
			//: which is the whole difference between the last two rows.
			if grant.Offline != tt.wantOffline {
				t.Errorf("Verify() grant.Offline = %t, want %t (%s)", grant.Offline, tt.wantOffline, tt.reason)
			}
		})
	}
}

// replaySeatedRoster builds a roster that lists the sample subject, so a row can
// vary the CI half without restating the device half.
//
// Its window is ten hours, which keeps it inside coreent.RosterLifetime and open
// at the instant every row verifies at — the rows are about supersession, and a
// document that also happened to be stale would prove nothing about it.
func replaySeatedRoster(issued time.Time, accounts map[string]coreent.CIEntitlementValue, relaxed bool) coreent.RosterValue {
	//: The device half is constant; the CI half is what the caller varies.
	return coreent.RosterValue{
		IssuedAt:   issued,
		ExpiresAt:  issued.Add(10 * time.Hour),
		Subjects:   map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: replayFingerprint}},
		CIAccounts: accounts,
		CIRelaxed:  relaxed,
	}
}

// Test_judgeMark pins the six answers the ratchet's comparison can give.
//
// Five of them are reachable through Verify and pinned end to end by
// Test_Verify_anOlderGenuineRosterCannotUndoADecisionAlreadyReached. The sixth is
// not: an offered document that proves nothing about time cannot arrive from
// rosterFrom, which calls rememberRoster one line after ParseBundle returned. It
// is asserted here because the answer it must give is the surprising one — do not
// install, and do NOT refuse — and because a future caller that skips
// authentication would otherwise discover it in production.
func Test_judgeMark(t *testing.T) {
	t.Parallel()

	base := time.Now().Truncate(time.Second)

	// present builds a record at issued whose payload digest is derived from
	// tag, so two records can share an instant and differ as statements.
	present := func(issued time.Time, tag byte) markRecord {
		return markRecord{issued: issued, payload: [32]byte{tag}, present: true}
	}

	tests := []struct {
		name string
		// offered is the record derived from the bundle just fetched.
		offered markRecord
		// held is the record derived from the cached bundle.
		held markRecord
		// wantInstall is whether the offered bytes may replace the cache.
		wantInstall bool
		// wantRefusal is whether the caller must not act on the document.
		wantRefusal bool
		reason      string
	}{
		{
			name:        "nothing held installs whatever is offered",
			offered:     present(base, 1),
			wantInstall: true,
			reason:      "a first fetch and a cleared cache have no decision to undo",
		},
		{
			name:        "nothing held installs even an unprovable document",
			offered:     markRecord{},
			wantInstall: true,
			reason:      "there is no mark to lower, so the cache is strictly better off with bytes in it",
		},
		{
			name:    "an unprovable document never lowers a mark that is held",
			offered: markRecord{},
			held:    present(base, 1),
			reason:  "installing it would replace a roster with something that is not one, and refusing would answer an unauthenticated caller with a refused licence",
		},
		{
			name:        "a newer instant installs",
			offered:     present(base.Add(time.Hour), 2),
			held:        present(base, 1),
			wantInstall: true,
			reason:      "fresher evidence is the point of fetching at all",
		},
		{
			name:        "the same statement at the same instant installs",
			offered:     present(base, 1),
			held:        present(base, 1),
			wantInstall: true,
			reason:      "every re-verification inside one publication interval offers the roster already cached",
		},
		{
			name:        "a different statement at the same instant is refused",
			offered:     present(base, 2),
			held:        present(base, 1),
			wantRefusal: true,
			reason:      "two documents signed for one moment are two statements, and the one already accepted is the decision",
		},
		{
			name:        "an older instant is refused",
			offered:     present(base.Add(-time.Hour), 2),
			held:        present(base, 1),
			wantRefusal: true,
			reason:      "this is the replay the ratchet exists for, and the half that used to reach the decision anyway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := judgeMark(tt.offered, tt.held)
			if got.install != tt.wantInstall {
				t.Errorf("judgeMark().install = %t, want %t (%s)", got.install, tt.wantInstall, tt.reason)
			}
			if (got.refusal != nil) != tt.wantRefusal {
				t.Fatalf("judgeMark().refusal = %v, want a refusal: %t (%s)", got.refusal, tt.wantRefusal, tt.reason)
			}
			//: Nothing more to check when the document may be acted on.
			if !tt.wantRefusal {
				return
			}
			//: One sentinel for both refusals, because the resolving action does
			//: not differ: publish, or fetch, a current roster.
			if !errors.Is(got.refusal, coreent.ErrRosterStale) {
				t.Errorf("judgeMark().refusal = %v, want coreent.ErrRosterStale (%s)", got.refusal, tt.reason)
			}
			//: And it must say WHERE, or a log cannot tell this refusal from a
			//: window that merely closed.
			if got := probeField(got.refusal, "stage"); got != markCompareStage {
				t.Errorf("judgeMark().refusal stage = %q, want %q (%s)", got, markCompareStage, tt.reason)
			}
		})
	}
}
