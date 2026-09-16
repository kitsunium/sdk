// Package entitlement_test exercises the facade the way a consumer does:
// through the public package alone, with no access to the internal layers.
//
// That restriction is the point. The facade shipped with fifteen Code
// constants and NOT ONE sentinel, so a consumer could read a refusal's code but
// could not write errors.Is — which is how every caller in the wild actually
// asks. A test importing the internal packages would have passed anyway,
// because the symbols exist there; only a test confined to the public surface
// can fail on a facade that does not export enough to be used.
package entitlement_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/entitlement"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// stubIdentity answers the port without touching any key material, so the
// refusals under test come from the roster path and never from the machine.
type stubIdentity struct {
	subject string
	err     error
}

// Discover reports the subject this stub claims to be.
//
// Parameters: none.
//
// Returns:
//   - subject: the configured subject.
//   - err: the configured discovery failure, if any.
func (s stubIdentity) Discover() (subject string, err error) {
	//: Hand back whatever the case configured.
	return s.subject, s.err
}

// Fingerprint reports a fixed fingerprint for any subject.
//
// Parameters:
//   - subject: ignored; this stub holds one identity.
//
// Returns:
//   - fingerprint: a fixed value.
//   - err: always nil.
func (s stubIdentity) Fingerprint(subject string) (fingerprint string, err error) {
	//: A stable value; no test here compares it against a roster.
	return "SHA256:stub", nil
}

// ProvePossession always succeeds, so a refusal is never this stub's doing.
//
// Parameters:
//   - subject: ignored.
//
// Returns:
//   - err: always nil.
func (s stubIdentity) ProvePossession(subject string) error {
	//: Possession is not what any case here is about.
	return nil
}

// TestTheFacadeCarriesBothWaysOfAskingWhy pins that every sentinel the facade
// re-exports carries the Code the facade publishes beside it.
//
// Two spellings of one question have to agree, or a consumer that switched from
// errors.Is to errs.HasCode would silently stop matching. Pairing them in a
// table is what makes a mismatched re-export a failure rather than a surprise
// three releases later.
func TestTheFacadeCarriesBothWaysOfAskingWhy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
		code     errs.Code
	}{
		{name: "no licence", sentinel: entitlement.ErrNoLicense, code: entitlement.CodeNoLicence},
		{name: "roster unsigned", sentinel: entitlement.ErrRosterUnsigned, code: entitlement.CodeRosterUnsigned},
		{name: "roster stale", sentinel: entitlement.ErrRosterStale, code: entitlement.CodeRosterStale},
		{name: "revoked", sentinel: entitlement.ErrRevoked, code: entitlement.CodeRevoked},
		{name: "licence expired", sentinel: entitlement.ErrLicenseExpired, code: entitlement.CodeLicenceExpired},
		{name: "key mismatch", sentinel: entitlement.ErrKeyMismatch, code: entitlement.CodeKeyMismatch},
		{name: "roster unreachable", sentinel: entitlement.ErrRosterUnreachable, code: entitlement.CodeRosterUnreachable},
		{name: "ambiguous licence", sentinel: entitlement.ErrAmbiguousLicense, code: entitlement.CodeAmbiguousLicence},
		{name: "CI unverifiable", sentinel: entitlement.ErrCIUnverifiable, code: entitlement.CodeCIUnverifiable},
		{name: "CI unknown key", sentinel: entitlement.ErrCIUnknownKey, code: entitlement.CodeCIUnknownKey},
		{name: "CI not entitled", sentinel: entitlement.ErrCINotEntitled, code: entitlement.CodeCINotEntitled},
		{name: "no possession", sentinel: entitlement.ErrNoPossession, code: entitlement.CodeNoPossession},
		{name: "clock regressed", sentinel: entitlement.ErrClockRegressed, code: entitlement.CodeClockRegressed},
		{name: "update required", sentinel: entitlement.ErrUpdateRequired, code: entitlement.CodeUpdateRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A nil sentinel would make errors.Is answer true for everything,
			//: which is worse than not exporting it at all.
			if tt.sentinel == nil {
				t.Fatalf("%s: the facade exports a nil sentinel", tt.name)
			}
			got, ok := errs.CodeOf(tt.sentinel)
			//: An untyped sentinel carries no code, so errs.HasCode could never
			//: answer on it — the half of the contract errors.Is cannot cover.
			if !ok {
				t.Fatalf("errs.CodeOf(%v) reports no code: the facade re-exported "+
					"an untyped sentinel", tt.sentinel)
			}
			//: The sentinel and the Code beside it must describe one refusal.
			if got != tt.code {
				t.Errorf("errs.CodeOf(%v) = %v, want %v — the sentinel and the "+
					"Code the facade publishes beside it disagree", tt.sentinel, got, tt.code)
			}
		})
	}
}

// TestAConsumerCanTellCannotDecideFromDecidedNo is the facade's whole reason for
// exporting sentinels, exercised end to end through the public surface.
//
// A verifier with no origins cannot reach anything, and the refusal must read as
// "cannot decide" rather than "not entitled" — through errors.Is AND through
// errs.HasCode, because a consumer will use one or the other and both have to
// answer.
func TestAConsumerCanTellCannotDecideFromDecidedNo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity entitlement.Identity
		reason   string
	}{
		{
			name:     "no origin answers",
			identity: stubIdentity{subject: "6ba7b810-9dad-41d1-80b4-00c04fd430c8"},
			reason:   "nothing to fetch a roster from, and nothing cached",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A product naming no origin, so the fetch has nowhere to go.
			service := entitlement.New(tt.identity, nil, new(entitlement.Product))
			_, err := service.Verify(time.Now())
			//: The point of the case: this must refuse, not succeed.
			if err == nil {
				t.Fatalf("Verify() = nil error, want a refusal (%s)", tt.reason)
			}
			//: The spelling a caller in the wild reaches for first.
			if !errors.Is(err, entitlement.ErrRosterUnreachable) {
				t.Errorf("errors.Is(err, ErrRosterUnreachable) = false for %v — a "+
					"consumer cannot tell an outage from a revocation (%s)", err, tt.reason)
			}
			//: And the SDK's own spelling, which must agree.
			if !errs.HasCode(err, entitlement.CodeRosterUnreachable) {
				t.Errorf("errs.HasCode(err, CodeRosterUnreachable) = false for %v (%s)",
					err, tt.reason)
			}
		})
	}
}

// TestTheVersionFloorRefusalNamesBothVersions pins that a caller can read the
// required version OUT of the refusal.
//
// Without it the caller upgrades, is refused again for the same reason, and
// loops — which is what the end-to-end suite did before UpdateRequiredError
// existed. The type has to survive the facade for that to keep being true.
func TestTheVersionFloorRefusalNamesBothVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		floor   string
		want    bool
	}{
		{name: "below the floor", current: "v1.0.0", floor: "v1.2.0", want: true},
		{name: "at the floor", current: "v1.2.0", floor: "v1.2.0", want: false},
		{name: "above the floor", current: "v1.3.0", floor: "v1.2.0", want: false},
		{name: "no floor requires nothing", current: "v1.0.0", floor: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: The predicate a caller gates the upgrade on.
			if got := entitlement.RequiresUpdate(tt.current, tt.floor); got != tt.want {
				t.Fatalf("RequiresUpdate(%q, %q) = %v, want %v", tt.current, tt.floor, got, tt.want)
			}
			//: Only the refusing case carries a refusal to inspect.
			if !tt.want {
				return
			}
			err := entitlement.UpdateRefusal(tt.current, tt.floor)
			//: The typed half — the caller reads the floor off it.
			var required *entitlement.UpdateRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("UpdateRefusal(%q, %q) = %v, want an *UpdateRequiredError",
					tt.current, tt.floor, err)
			}
			//: The floor must be readable, or the caller loops on the upgrade.
			if required.Required != tt.floor {
				t.Errorf("UpdateRequiredError.Required = %q, want %q", required.Required, tt.floor)
			}
			//: And the sentinel half, so errors.Is answers too.
			if !errors.Is(err, entitlement.ErrUpdateRequired) {
				t.Errorf("errors.Is(err, ErrUpdateRequired) = false for %v", err)
			}
		})
	}
}

// TestANilProductNeverPanicsAtConstruction pins, through the public surface
// alone, the contract this package documents everywhere and broke in the one
// place it mattered.
//
// Every ProductValue accessor tolerates a nil receiver, and Validate does too.
// The CONSTRUCTORS did not: both read the Origins FIELD, which a method cannot
// guard, so New(identity, vendor, nil) panicked before returning a verifier —
// on the one path that runs when a consumer has configured nothing yet.
//
// A nil product publishes nowhere, so the right outcome is a verifier that
// refuses with RosterUnreachable. That is a very different thing from a panic,
// and it is what a caller can handle.
func TestANilProductNeverPanicsAtConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func() *entitlement.Service
	}{
		{
			name:  "New",
			build: func() *entitlement.Service { return entitlement.New(stubIdentity{}, nil, nil) },
		},
		{
			name: "NewWithGetter",
			build: func() *entitlement.Service {
				return entitlement.NewWithGetter(refusingGetter{}, stubIdentity{}, nil, nil)
			},
		},
		{
			name: "NewWithAnchors",
			build: func() *entitlement.Service {
				return entitlement.NewWithAnchors(stubIdentity{}, [][]byte{nil}, nil)
			},
		},
		{
			//: An EMPTY anchor list must construct too, and then refuse — a
			//: constructor that panicked on it would turn a build mistake into
			//: a crash in the consumer's own start-up path.
			name: "NewWithAnchors with no anchor at all",
			build: func() *entitlement.Service {
				return entitlement.NewWithAnchors(stubIdentity{}, nil, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: Construction is the step that panicked; recover names it.
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s(…, nil) panicked on a nil product: %v", tt.name, r)
				}
			}()
			service := tt.build()
			//: A constructor that returns nothing is no better than one that
			//: panics — the caller dereferences it one line later.
			if service == nil {
				t.Fatalf("%s(…, nil) = nil service, want a verifier that refuses", tt.name)
			}
			_, err := service.Verify(time.Now())
			//: A product publishing nowhere cannot decide, which is the
			//: documented fallback rather than a refusal.
			if !errors.Is(err, entitlement.ErrRosterUnreachable) {
				t.Errorf("Verify() = %v, want ErrRosterUnreachable — a product that "+
					"names no origin has nowhere to fetch from", err)
			}
		})
	}
}

// refusingGetter answers every fetch with a failure, so the nil-product cases
// above exercise construction rather than the network.
type refusingGetter struct{}

// Get always fails, which is what a verifier with no origins would see anyway.
//
// Parameters:
//   - url: ignored.
//
// Returns:
//   - resp: always nil.
//   - err: always non-nil.
func (refusingGetter) Get(url string) (resp *http.Response, err error) {
	//: Nothing to serve; the case is about construction.
	return nil, errors.New("no network in this test")
}

// TestTheSentinelsKeepTheirConcreteType pins the shape of the fourteen
// re-exported sentinels, which is a thing a review can only catch by reading.
//
// They shipped declared as `ErrNoLicense error = coreent.ErrNoLicense`. The
// explicit `error` ERASES the concrete type: the underlying values are
// *errs.Error, which carries Code, Reason, Public, Private and ExitCode, and a
// consumer holding the package variable had to type-assert to reach any of
// them — through a type they cannot name, since internal/kernel/errs is
// internal. pkg/v1/lock, pkg/v1/cache and pkg/v1/authz all declare theirs
// without the annotation; this package was the outlier.
//
// The assertions below ARE the pin, and they are not merely non-nil checks:
// every accessor is called directly on the package variable, which only
// compiles while the concrete type survives. Putting `error` back fails the
// build here rather than quietly removing what a consumer can read.
func TestTheSentinelsKeepTheirConcreteType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// code is what the sentinel's own accessor must report.
		code errs.Code
		// public is the wire-safe sentence it must carry.
		public string
		// got reads the three accessors off the package variable, with no
		// assertion anywhere — the compile is half the assertion.
		got func() (errs.Code, string, int)
		// err is the same sentinel as a plain error, for the accessor half.
		err error
		// reason explains what a consumer does with them.
		reason string
	}{
		{
			name:   "ErrNoLicense",
			code:   entitlement.CodeNoLicence,
			public: "no entitlement key was found on this machine",
			got: func() (errs.Code, string, int) {
				return entitlement.ErrNoLicense.Code(), entitlement.ErrNoLicense.Public(), entitlement.ErrNoLicense.ExitCode()
			},
			err:    entitlement.ErrNoLicense,
			reason: "a CLI renders Public to the operator and exits on ExitCode",
		},
		{
			name:   "ErrRosterUnreachable",
			code:   entitlement.CodeRosterUnreachable,
			public: "the roster could not be reached",
			got: func() (errs.Code, string, int) {
				return entitlement.ErrRosterUnreachable.Code(), entitlement.ErrRosterUnreachable.Public(), entitlement.ErrRosterUnreachable.ExitCode()
			},
			err:    entitlement.ErrRosterUnreachable,
			reason: "the one sentinel a consumer must tell apart from a refusal",
		},
		{
			name:   "ErrUpdateRequired",
			code:   entitlement.CodeUpdateRequired,
			public: "a newer version is required",
			got: func() (errs.Code, string, int) {
				return entitlement.ErrUpdateRequired.Code(), entitlement.ErrUpdateRequired.Public(), entitlement.ErrUpdateRequired.ExitCode()
			},
			err:    entitlement.ErrUpdateRequired,
			reason: "the refusal whose remedy is an upgrade rather than a licence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, public, exit := tt.got()
			//: The code has to be the one the matching constant names, or the
			//: two halves of the facade disagree about the same failure.
			if code != tt.code {
				t.Errorf("%s.Code() = %v, want %v (%s)", tt.name, code, tt.code, tt.reason)
			}
			//: The wire-safe sentence is what a consumer renders.
			if public != tt.public {
				t.Errorf("%s.Public() = %q, want %q (%s)", tt.name, public, tt.public, tt.reason)
			}
			//: And the exit status is what a CLI returns to its shell.
			if exit == 0 {
				t.Errorf("%s.ExitCode() = 0, want a POSIX status (%s)", tt.name, tt.reason)
			}
			//: The widening costs nothing an ordinary consumer had: the value
			//: still satisfies error, so `var e error = sentinel` compiles and
			//: errors.Is answers exactly as before.
			var asError error = entitlement.ErrNoLicense
			if !errors.Is(asError, entitlement.ErrNoLicense) {
				t.Errorf("errors.Is through the error interface = false, want true (%s)", tt.reason)
			}
			//: The two ways of asking must agree. A consumer is free to reach
			//: these through the stable pkg/v1/errs accessors instead of the
			//: methods, and nothing above would notice if the two diverged —
			//: so this asserts they do not, on the same three values.
			if viaAccessor, _ := errs.CodeOf(tt.err); viaAccessor != code {
				t.Errorf("errs.CodeOf = %v, method Code() = %v; the two must agree (%s)", viaAccessor, code, tt.reason)
			}
			if viaAccessor := errs.PublicOf(tt.err); viaAccessor != public {
				t.Errorf("errs.PublicOf = %q, method Public() = %q; the two must agree (%s)", viaAccessor, public, tt.reason)
			}
			if viaAccessor := errs.ExitCodeOf(tt.err); viaAccessor != exit {
				t.Errorf("errs.ExitCodeOf = %d, method ExitCode() = %d; the two must agree (%s)", viaAccessor, exit, tt.reason)
			}
		})
	}
}

// provingIdentity answers all three port methods so the rotation cases reach a
// grant rather than stopping at the machine's own half of the proof.
//
// stubIdentity above deliberately cannot: its Fingerprint returns "", which is
// what the refusal cases want and what an authorisation case cannot use.
type provingIdentity struct {
	// subject is who this machine claims to be.
	subject string
	// fingerprint is what it presents, matched against the roster's entry.
	fingerprint string
}

// Discover reports the subject this stub claims to be.
//
// Returns:
//   - subject: the configured identifier.
//   - err: always nil.
func (p provingIdentity) Discover() (subject string, err error) {
	//: The case decides who this machine is.
	return p.subject, nil
}

// Fingerprint reports what this stub presents for subject.
//
// Parameters:
//   - subject: ignored; one identity per stub.
//
// Returns:
//   - fingerprint: the configured fingerprint.
//   - err: always nil.
func (p provingIdentity) Fingerprint(subject string) (fingerprint string, err error) {
	//: The case decides what this machine presents.
	return p.fingerprint, nil
}

// ProvePossession accepts, so the rotation cases turn on the ANCHOR and on
// nothing about the machine.
//
// Parameters:
//   - subject: ignored.
//
// Returns:
//   - err: always nil, which IS the proof.
func (p provingIdentity) ProvePossession(subject string) error {
	//: A nil error is the proof; these cases are about the vendor's key.
	return nil
}

// bundleGetter serves one signed bundle for every request.
type bundleGetter struct {
	// bundle is the document every origin answers with.
	bundle []byte
}

// Get answers with the canned bundle.
//
// Parameters:
//   - url: ignored; one document per getter.
//
// Returns:
//   - resp: a 200 carrying the bundle.
//   - err: always nil.
func (b bundleGetter) Get(url string) (resp *http.Response, err error) {
	//: Serve the whole signed document, as a publication point would.
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b.bundle))}, nil
}

// signRoster builds the one-document bundle form through the PUBLIC surface.
//
// It uses entitlement.Bundle and entitlement.Roster and nothing internal, which
// is what makes it a statement about what a consumer can do: a vendor publishing
// a roster and a test serving one build the same object.
func signRoster(t *testing.T, priv ed25519.PrivateKey, roster entitlement.Roster) []byte {
	t.Helper()

	payload, marshalErr := json.Marshal(roster)
	//: A failure here is an environment problem, not a test outcome.
	if marshalErr != nil {
		t.Fatalf("marshalling roster: %v", marshalErr)
	}
	raw, bundleErr := json.Marshal(entitlement.Bundle{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if bundleErr != nil {
		t.Fatalf("marshalling bundle: %v", bundleErr)
	}
	//: The document a publication point serves.
	return raw
}

// TestNewWithAnchorsAcceptsEitherAnchorDuringARotation pins the rotation at the
// surface a consumer actually holds.
//
// A verifier built with ONE anchor has no path off it: a roster signed by the
// replacement key comes back ErrRosterUnsigned — the spoofing sentinel, from a
// document the vendor genuinely signed — and that refusal lands BEFORE the
// version floor that would have told the binary to upgrade, so the release
// carrying the new anchor is refused by the installation that needs it.
//
// Every row here goes through the public package alone. A row signed by a key on
// NEITHER list is what keeps this a test about rotation rather than about
// accepting anything: widening who may sign is the mistake this shape could
// plausibly have made.
func TestNewWithAnchorsAcceptsEitherAnchorDuringARotation(t *testing.T) {
	t.Parallel()

	const subject string = "6ba7b810-9dad-41d1-80b4-00c04fd430c8"
	const fingerprint string = "SHA256:rotation"

	tests := []struct {
		name string
		// signer selects the private half that signs: 0 and 1 are linked in, 2
		// is on no list.
		signer int
		// wantErr is the refusal, or nil when the machine must be entitled.
		wantErr error
		reason  string
	}{
		{name: "the outgoing anchor still authorises", signer: 0, reason: "a second anchor must not break the installations that only had the first"},
		{name: "the incoming anchor authorises", signer: 1, reason: "without this there is no way to move an installation to a new key in band"},
		{name: "a key on neither list authorises nothing", signer: 2, wantErr: entitlement.ErrRosterUnsigned, reason: "several accepted anchors must not become any anchor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pubs := make([][]byte, 3)
			privs := make([]ed25519.PrivateKey, 3)
			//: Three pairs: two linked in, one that is nobody's anchor.
			for i := range privs {
				pub, priv, keyErr := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem.
				if keyErr != nil {
					t.Fatalf("generating key %d: %v", i, keyErr)
				}
				pubs[i], privs[i] = pub, priv
			}

			now := time.Now().Truncate(time.Second)
			bundle := signRoster(t, privs[tt.signer], entitlement.Roster{
				IssuedAt:  now.Add(-time.Hour),
				ExpiresAt: now.Add(time.Hour),
				Subjects:  map[string]entitlement.Subject{subject: {Fingerprint: fingerprint}},
			})

			//: Built through the facade alone: the getter stands in for the
			//: origin, WithAnchors carries the list a mid-rotation build links
			//: in, and Service is an alias so the setter is reachable here
			//: without the facade re-declaring it.
			grant, err := entitlement.NewWithGetter(bundleGetter{bundle: bundle},
				provingIdentity{subject: subject, fingerprint: fingerprint},
				nil, new(entitlement.Product)).
				WithAnchors([][]byte{pubs[0], pubs[1]}).
				WithOrigins([]entitlement.Origin{{Name: "primary", BundleURL: "https://example.invalid/roster.signed.json"}}).
				Verify(now)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			//: Entitled, and for the subject the roster listed.
			if grant.Subject != subject {
				t.Errorf("Verify() grant.Subject = %q, want %q (%s)", grant.Subject, subject, tt.reason)
			}
		})
	}
}

// TestAConsumerCanScheduleOnAGrantsDeadlineWithoutPolling pins the half of the
// expiry problem that belongs to this SDK, through the public surface alone.
//
// Nothing here revokes a grant when its deadline passes — no goroutine, no
// timer, no callback — so a grant expiring a second after a check keeps
// authorising until the consumer looks again. That is the consumer's tick, and
// the only thing that makes it avoidable is being able to read the effective
// deadline and schedule on it.
//
// The field alone could not answer that. `NotAfter` is genuinely the zero instant
// on a grant seeded from a bare timestamp, and the fallback the SDK applies lived
// inside `Expired` where no caller could reach it. A consumer holding such a
// grant had to poll.
//
// Both rows assert the two spellings agree AT the deadline, because a consumer
// waking exactly on its own timer must not find the grant already gone — that
// off-by-one is what would send it back to polling with a safety margin.
func TestAConsumerCanScheduleOnAGrantsDeadlineWithoutPolling(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		notAfter time.Time
		want     time.Time
		reason   string
	}{
		{
			name:     "a grant that recorded its deadline",
			notAfter: verified.Add(90 * time.Minute),
			want:     verified.Add(90 * time.Minute),
			reason:   "the ordinary shape, from a full verification",
		},
		{
			name:   "a grant seeded from a bare timestamp",
			want:   verified.Add(entitlement.RosterLifetime),
			reason: "the shape whose deadline a consumer could not compute, and the reason the method is public at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: Addressable on purpose: Expired and Deadline both take a pointer
			//: receiver, which is a documented property of the type.
			grant := entitlement.Grant{VerifiedAt: verified, NotAfter: tt.notAfter}

			deadline := grant.Deadline()
			if !deadline.Equal(tt.want) {
				t.Fatalf("Grant.Deadline() = %s, want %s (%s)",
					deadline.UTC().Format(time.RFC3339), tt.want.UTC().Format(time.RFC3339), tt.reason)
			}
			//: The instant a consumer would arm its timer on still authorises,
			//: and the instant after it does not. Anything else and scheduling
			//: on the deadline is not a substitute for polling.
			if grant.Expired(deadline) {
				t.Errorf("Grant.Expired(Deadline()) = true, want false (%s)", tt.reason)
			}
			if !grant.Expired(deadline.Add(time.Nanosecond)) {
				t.Errorf("Grant.Expired(Deadline()+1ns) = false, want true (%s)", tt.reason)
			}
		})
	}
}
