package entitlement_test

import (
	"cmp"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"

	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// ciSeatKeyBits matches the smallest modulus the verifier accepts.
const ciSeatKeyBits int = 2048

// ciAccountID is the account the roster covers in these tests.
const ciAccountID string = "133899878"

// sampleUUID is a well-formed subject identifier used across the fixtures.
const sampleUUID string = "11111111-2222-3333-4444-555555555555"

// otherUUID is a second, unrelated identity. Discovery must refuse to choose
// between two usable ones rather than let sort order decide.
const otherUUID string = "99999999-8888-4777-8666-555544443333"

// stubGetter answers roster requests with one canned bundle, so admission
// logic is exercised without a network.
type stubGetter struct {
	// bundle is the signed document served for every request.
	bundle []byte
	// status is the HTTP status returned for every request.
	status int
	// fail makes every request fail at the transport layer.
	fail bool
}

// Get serves the canned bundle.
func (s *stubGetter) Get(_ string) (*http.Response, error) {
	//: A transport failure models an unreachable roster.
	if s.fail {
		return nil, errors.New("dial refused")
	}
	//: Default to 200 so tests only declare the interesting statuses.
	code := cmp.Or(s.status, http.StatusOK)
	//: Return the canned bundle with the requested status.
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(string(s.bundle))),
	}, nil
}

// enrol writes a keypair named after uuid and returns its fingerprint.
func enrol(t *testing.T, dir, uuid string) string {
	t.Helper()

	pub, _ := writeKeyPair(t, dir, uuid, ownerOnly)
	//: The roster records fingerprints, not raw keys.
	return entitlement.Fingerprint(pub)
}

// publishRoster signs a roster listing the given subjects.
func publishRoster(t *testing.T, subjects map[string]coreent.SubjectValue, iat, exp time.Time) (getter *stubGetter, vendor ed25519.PublicKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("generating vendor key: %v", err)
	}
	raw, err := json.Marshal(coreent.RosterValue{IssuedAt: iat, ExpiresAt: exp, Subjects: subjects})
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling roster: %v", err)
	}
	bundle, err := json.Marshal(svcent.BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling bundle: %v", err)
	}
	//: Return a fetcher serving that exact signed bundle.
	return &stubGetter{bundle: bundle}, pub
}

// TestDiscoverSubject pins that the filename carries the identity, which is
// what removes the need for any local state file.
func TestDiscoverSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		// dirs are created as DIRECTORIES wearing the same names. They stat
		// without error, so nothing but an explicit regular-file test keeps
		// them from counting as an identity.
		dirs []string
		// links are symlinks pointing at a real key file. Both halves must
		// treat them the same way: the published half used to be checked with
		// DirEntry.Type (which does NOT follow a link) and the private half
		// with os.Stat (which does), so a symlinked pair was half-accepted.
		links   []string
		want    string
		wantErr error
	}{
		{name: "a uuid-named public key is the enrolment", files: []string{sampleUUID + ".pub"}, want: sampleUUID},
		{name: "unrelated keys are ignored", files: []string{"id_ed25519.pub", "known_hosts"}, wantErr: coreent.ErrNoLicense},
		{name: "a private half alone is not an enrolment", files: []string{sampleUUID}, wantErr: coreent.ErrNoLicense},
		{name: "an empty directory is the never-enrolled case", files: nil, wantErr: coreent.ErrNoLicense},
		{
			//: A stray .pub used to be able to WIN the selection purely by
			//: sorting first, silently deciding which licence got verified,
			//: which one a rotation overwrote and which one status reported.
			//: The one identity this machine can answer for is the answer.
			name:  "a stray published half does not outrank the usable identity",
			files: []string{"00000000-0000-4000-8000-000000000000.pub", sampleUUID, sampleUUID + ".pub"},
			want:  sampleUUID,
		},
		{
			name:    "two usable identities are refused rather than guessed",
			files:   []string{sampleUUID, sampleUUID + ".pub", otherUUID, otherUUID + ".pub"},
			wantErr: coreent.ErrAmbiguousLicense,
		},
		{
			//: Neither candidate is usable, so there is nothing to prefer;
			//: guessing here would be exactly the old behaviour.
			name:    "several published halves with no private key are ambiguous",
			files:   []string{sampleUUID + ".pub", otherUUID + ".pub"},
			wantErr: coreent.ErrAmbiguousLicense,
		},
		{
			//: A directory named <uuid>.pub stats without error. Counting it
			//: would manufacture an ambiguity against the one real identity
			//: and lock the machine out over something that never held a key.
			name:  "a directory named like a published half is not an identity",
			files: []string{sampleUUID, sampleUUID + ".pub"},
			dirs:  []string{otherUUID + ".pub"},
			want:  sampleUUID,
		},
		{
			//: Same reasoning on the private side: a pair is not complete
			//: just because something answers to the name.
			name:  "a directory standing in for a private half is not usable",
			files: []string{sampleUUID, sampleUUID + ".pub", otherUUID + ".pub"},
			dirs:  []string{otherUUID},
			want:  sampleUUID,
		},
		{
			//: A key symlinked in from a password manager or a mounted volume
			//: is an ordinary setup. Both halves follow links, so the pair is
			//: usable — refusing it would break installs that work today.
			name:  "a symlinked pair is a usable identity",
			links: []string{sampleUUID, sampleUUID + ".pub"},
			want:  sampleUUID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(dir+"/"+f, []byte("x"), ownerOnly); err != nil {
					t.Fatalf("writing fixture %s: %v", f, err)
				}
			}
			for _, d := range tt.dirs {
				if err := os.Mkdir(dir+"/"+d, 0o700); err != nil {
					t.Fatalf("creating fixture dir %s: %v", d, err)
				}
			}
			for _, l := range tt.links {
				//: The target lives outside the scanned directory, so only the
				//: link itself can make the identity discoverable.
				target := filepath.Join(t.TempDir(), "real-"+l)
				if err := os.WriteFile(target, []byte("x"), ownerOnly); err != nil {
					t.Fatalf("writing link target %s: %v", target, err)
				}
				if err := os.Symlink(target, dir+"/"+l); err != nil {
					t.Fatalf("creating fixture link %s: %v", l, err)
				}
			}

			got, err := entitlement.DiscoverSubject(dir)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("entitlement.DiscoverSubject() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("entitlement.DiscoverSubject() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("entitlement.DiscoverSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestService_Verify walks the whole admission chain, including the cases
// that separate "cannot decide" from "decided no".
func TestService_Verify(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		enrolled       bool
		listed         bool
		wrongFP        bool
		expired        bool
		subjectExpired bool
		badSig         bool
		offline        bool
		status         int
		wantErr        error
	}{
		{name: "an enrolled and listed subject is granted", enrolled: true, listed: true},
		{name: "an unenrolled machine has no identity", listed: true, wantErr: coreent.ErrNoLicense},
		{name: "a subject absent from the roster is revoked", enrolled: true, wantErr: coreent.ErrRevoked},
		{name: "a local key that does not match the roster is refused", enrolled: true, listed: true, wrongFP: true, wantErr: coreent.ErrKeyMismatch},
		{name: "an expired roster cannot authorize", enrolled: true, listed: true, expired: true, wantErr: coreent.ErrRosterStale},
		{name: "a subject past its own term is refused despite a fresh roster", enrolled: true, listed: true, subjectExpired: true, wantErr: coreent.ErrLicenseExpired},
		{name: "a roster signed by an impostor is refused", enrolled: true, listed: true, badSig: true, wantErr: coreent.ErrRosterUnsigned},
		{name: "an unreachable roster yields no decision", enrolled: true, listed: true, offline: true, wantErr: coreent.ErrRosterUnreachable},
		{name: "a non-200 answer yields no decision", enrolled: true, listed: true, status: http.StatusNotFound, wantErr: coreent.ErrRosterUnreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			fingerprint := "SHA256:not-the-local-key"
			//: Only an enrolled machine carries key material at all.
			if tt.enrolled {
				fingerprint = enrol(t, dir, sampleUUID)
			}
			//: A mismatch models a key swapped behind the subject's back.
			if tt.wrongFP {
				fingerprint = "SHA256:someone-else"
			}

			subjects := map[string]coreent.SubjectValue{}
			//: An unlisted subject is exactly what revocation looks like.
			if tt.listed {
				sv := coreent.SubjectValue{Fingerprint: fingerprint}
				//: A term in the past models a subject that outlived its own
				//: entitlement even though the roster itself stays fresh.
				if tt.subjectExpired {
					sv.ExpiresAt = now.Add(-time.Minute)
				}
				subjects[sampleUUID] = sv
			}

			iat, exp := now.Add(-time.Hour), now.Add(coreent.RosterLifetime-time.Hour)
			//: A closed window must refuse even a well-signed roster.
			if tt.expired {
				iat, exp = now.Add(-2*time.Hour), now.Add(-time.Hour)
			}

			getter, vendor := publishRoster(t, subjects, iat, exp)
			getter.status = tt.status
			getter.fail = tt.offline
			//: Verifying against another key models a substituted endpoint.
			if tt.badSig {
				other, _, err := ed25519.GenerateKey(nil)
				if err != nil {
					t.Fatalf("generating impostor key: %v", err)
				}
				vendor = other
			}

			svc := svcent.NewServiceWithGetter(getter, entitlement.NewSSHIdentity(dir), vendor, &testProduct)
			grant, err := svc.Verify(now)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Verify() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil", err)
			}
			if grant.Subject != sampleUUID {
				t.Errorf("Verify() subject = %q, want %q", grant.Subject, sampleUUID)
			}
		})
	}
}

// TestNewService pins that the default constructor is usable without any
// injection, since that is the path the binary itself takes.
func TestNewService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sshDir string
		vendor []byte
	}{
		{name: "conventional directory", sshDir: "/home/u/.ssh", vendor: make([]byte, ed25519.PublicKeySize)},
		{name: "empty vendor key is still constructible", sshDir: "/tmp", vendor: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if svc := svcent.NewService(entitlement.NewSSHIdentity(tt.sshDir), tt.vendor, &testProduct); svc == nil {
				t.Error("NewService() returned nil")
			}
		})
	}
}

// TestNewServiceWithGetter pins that an injected getter is honoured, which is
// what keeps the admission tests free of a network.
func TestNewServiceWithGetter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		offline bool
		wantErr error
	}{
		{name: "the injected getter is the one consulted", offline: true, wantErr: coreent.ErrRosterUnreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			enrol(t, dir, sampleUUID)
			svc := svcent.NewServiceWithGetter(&stubGetter{fail: tt.offline}, entitlement.NewSSHIdentity(dir), make([]byte, ed25519.PublicKeySize), &testProduct)
			_, err := svc.Verify(time.Now())
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Verify() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestDefaultSSHDir pins that a key location is always produced: returning an
// empty path would make the caller fail with a confusing error far from here.
func TestDefaultSSHDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "always yields a non-empty path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := entitlement.DefaultSSHDir(); got == "" {
				t.Error("entitlement.DefaultSSHDir() = \"\", want a usable path")
			}
		})
	}
}

// TestService_VerifyPossessionFailure pins that the possession proof is
// reached and enforced through the full Verify path, not merely unit-tested
// in isolation. Without the private half a caller holds only what the roster
// publishes to everyone, which must never be enough.
//
// The loose-permissions half of that guard is asserted in
// service_unix_external_test.go: os.Chmod cannot widen an ACL on Windows, so
// the fixture cannot even create the state there.
func TestService_VerifyPossessionFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		removeKey bool
		wantErr   error
	}{
		{name: "a deleted private half cannot answer the challenge", removeKey: true, wantErr: coreent.ErrNoLicense},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			//: Removing the private half leaves exactly what the roster
			//: publishes: the public key and nothing else.
			if tt.removeKey {
				if err := os.Remove(entitlement.PrivateKeyPath(dir, sampleUUID)); err != nil {
					t.Fatalf("removing private key: %v", err)
				}
			}

			getter, vendor := publishRoster(t, map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}}, now.Add(-time.Hour), now.Add(coreent.RosterLifetime-time.Hour))
			_, err := svcent.NewServiceWithGetter(getter, entitlement.NewSSHIdentity(dir), vendor, &testProduct).Verify(now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Verify() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestService_VerifyOversizedArtefact pins the read bound. The endpoint is
// untrusted by construction, so an unbounded read hands it a
// memory-exhaustion lever rather than merely a wrong answer.
func TestService_VerifyOversizedArtefact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{name: "an oversized roster is refused rather than buffered", size: (4 << 20) + 1, wantErr: coreent.ErrRosterUnreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			enrol(t, dir, sampleUUID)
			flood := &stubGetter{bundle: make([]byte, tt.size)}
			_, err := svcent.NewServiceWithGetter(flood, entitlement.NewSSHIdentity(dir), make([]byte, 32), &testProduct).Verify(time.Now())
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Verify() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Test_Service_WithVersion pins that declaring a version is what lets a
// binary CLEAR the mandatory-update floor — and that not declaring one no
// longer opts out of it.
//
// It is a separate call rather than a constructor parameter because
// pkg/license must not import the command package that owns the version
// string; this test is what keeps that seam honest. The undeclared row is the
// enforcement half: a chainable setter is one forgotten call away, and
// `license status` had already forgotten it, so "no version declared" has to
// mean "cannot clear the bar" rather than "no bar applies".
func Test_Service_WithVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		declare bool
		version string
		floor   string
		wantErr bool
		reason  string
	}{
		{name: "a declared out-of-date version is refused", declare: true, version: "v1.0.0", floor: "v2.0.0", wantErr: true, reason: "declaring the version is what arms the floor"},
		{name: "a declared current version passes", declare: true, version: "v2.0.0", floor: "v2.0.0", wantErr: false, reason: "the floor is inclusive"},
		{name: "not declaring a version does not opt out of the floor", declare: false, floor: "v2.0.0", wantErr: true, reason: "a forgotten WithVersion must not disable the floor in silence"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			now := time.Now()

			pair := signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:        now.Add(-time.Hour),
				ExpiresAt:       now.Add(time.Hour),
				Subjects:        map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
				RequiredVersion: tt.floor,
			})
			getter := &multiOriginGetter{
				states:  map[string]originState{"solo": originHealthy},
				current: pair,
			}

			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo"))
			//: Only the declaring cases arm the floor.
			if tt.declare {
				svc = svc.WithVersion(tt.version)
			}

			_, verifyErr := svc.Verify(now)

			if (verifyErr != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, want error = %v (%s)", verifyErr, tt.wantErr, tt.reason)
			}
			//: When it refuses, it must be for the update — not because the
			//: licence itself broke.
			if tt.wantErr && !errors.Is(verifyErr, coreent.ErrUpdateRequired) {
				t.Errorf("Verify() error = %v, want coreent.ErrUpdateRequired (%s)", verifyErr, tt.reason)
			}
		})
	}
}

// seatFixture is a complete, signed world: a vendor key, a roster listing one
// CI account, and GitHub's published key set with a matching private half.
type seatFixture struct {
	// vendor is the anchor a svcent.Service verifies the roster against.
	vendor ed25519.PublicKey
	// bundle is the signed roster document the origin serves.
	bundle []byte
	// jwks is the key set GitHub publishes.
	jwks []byte
	// signer is the private half of the published key.
	signer *rsa.PrivateKey
}

// newSeatFixture builds the signed documents a full CI verification needs,
// around the roster the caller describes.
//
// Everything is real: a genuine ed25519 signature over the roster and a
// genuine RS256 signature over the token. Stubbing either would leave the
// wiring under test unexercised, which is the only thing this file is for.
//
// It takes the whole coreent.RosterValue rather than the CI block alone because the
// interesting cases are the ones where the OTHER fields matter too — a roster
// that lists a subject AND covers no CI account is what proves a strict
// refusal is refusing rather than merely finding nothing to fall back on.
func newSeatFixture(t *testing.T, roster coreent.RosterValue) *seatFixture {
	t.Helper()

	vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
	if keyErr != nil {
		t.Fatalf("generating vendor key: %v", keyErr)
	}
	raw, marshalErr := json.Marshal(roster)
	if marshalErr != nil {
		t.Fatalf("marshalling roster: %v", marshalErr)
	}
	bundle, bundleErr := json.Marshal(svcent.BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(vendorPriv, raw)),
	})
	if bundleErr != nil {
		t.Fatalf("marshalling bundle: %v", bundleErr)
	}

	signer, signerErr := rsa.GenerateKey(nil, ciSeatKeyBits)
	if signerErr != nil {
		t.Fatalf("generating signing key: %v", signerErr)
	}
	jwks, jwksErr := json.Marshal(svcent.JWKSValue{Keys: []svcent.JWKValue{{
		KeyType:   "RSA",
		KeyID:     "k1",
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(signer.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(bigBytes(signer.E)),
	}}})
	if jwksErr != nil {
		t.Fatalf("marshalling key set: %v", jwksErr)
	}

	return &seatFixture{vendor: vendorPub, bundle: bundle, jwks: jwks, signer: signer}
}

// bigBytes renders an exponent the way a JWK carries one: minimal big-endian.
func bigBytes(exponent int) []byte {
	var out []byte
	for exponent > 0 {
		out = append([]byte{byte(exponent & 0xff)}, out...)
		exponent >>= 8
	}
	return out
}

// get serves the roster bundle and the key set from the same fixture, so a
// svcent.Service can be pointed at it without a network.
func (f *seatFixture) get(url string) (*http.Response, error) {
	body := f.bundle
	if strings.Contains(url, "jwks") {
		body = f.jwks
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

// mintToken signs a token the way GitHub does, for the account given.
func (f *seatFixture) mintToken(t *testing.T, now time.Time, ownerID string) string {
	t.Helper()

	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshalling segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	header := encode(map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	claims := map[string]any{
		"iss": svcent.ActionsIssuer,
		"aud": svcent.DefaultActionsAudience,
		"sub": "repo:kodflow/ktn-linter:ref:refs/heads/main",
	}
	maps.Copy(claims, map[string]any{
		"repository":          "kodflow/ktn-linter",
		"repository_owner":    "kodflow",
		"repository_owner_id": ownerID,
	})
	maps.Copy(claims, map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(4 * time.Minute).Unix(),
	})
	payload := encode(claims)
	digest := sha256.Sum256([]byte(header + "." + payload))
	signature, err := rsa.SignPKCS1v15(nil, f.signer, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// TestServiceVerifyCISeat pins the wiring end to end: a CI run with no key on
// disk is authorised, and every way that should fail refuses rather than
// granting anything.
//
// "Falls back to the device path" is what these rows used to assert, and the
// inversion is why they no longer do: inside Actions the seat is required, so a
// failure there is the answer rather than a prelude to looking for a key on a
// machine that is not supposed to have one.
//
// This is the only test that exercises Verify's CI branch against genuinely
// signed documents. Everything below it is stubbed at a lower level, which
// says nothing about whether the branch is reached at all.
func TestServiceVerifyCISeat(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// accounts is what the roster grants.
		accounts map[string]coreent.CIEntitlementValue
		// tokenOwner is the account the minted token names.
		tokenOwner string
		// inCI controls whether the runner variables are present.
		inCI bool
		// wantSubject is the grant's subject when one is expected.
		wantSubject string
		wantErr     error
		reason      string
	}{
		{
			name:        "a covered CI run needs no device",
			accounts:    map[string]coreent.CIEntitlementValue{ciAccountID: {}},
			tokenOwner:  ciAccountID,
			inCI:        true,
			wantSubject: "ci:kodflow/ktn-linter",
			reason:      "the whole point: a runner holds no key and still runs",
		},
		{
			name:       "an uncovered account is refused, not fallen back from",
			accounts:   map[string]coreent.CIEntitlementValue{"999": {}},
			tokenOwner: ciAccountID,
			inCI:       true,
			wantErr:    coreent.ErrCINotEntitled,
			reason:     "a genuine run belonging to nobody's licence gets nothing, and since the seat is required in Actions the CI cause is what gets reported",
		},
		{
			name:       "an expired entitlement is refused",
			accounts:   map[string]coreent.CIEntitlementValue{ciAccountID: {ExpiresAt: now.Add(-time.Hour)}},
			tokenOwner: ciAccountID,
			inCI:       true,
			wantErr:    coreent.ErrCINotEntitled,
			reason:     "CI stops when the licence does, and stopping now means refusing rather than looking for a device key on a runner",
		},
		{
			name:       "a roster with no CI block refuses every CI run",
			accounts:   nil,
			tokenOwner: ciAccountID,
			inCI:       true,
			wantErr:    coreent.ErrCINotEntitled,
			reason:     "publishing a `ci` block is the deployment step the strict default requires; its absence fails loudly instead of quietly reopening the hole",
		},
		{
			name:       "outside CI the device path decides",
			accounts:   map[string]coreent.CIEntitlementValue{ciAccountID: {}},
			tokenOwner: ciAccountID,
			inCI:       false,
			wantErr:    coreent.ErrNoLicense,
			reason:     "an entitled account does not authorise a laptop with no key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSeatFixture(t, coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(coreent.RosterLifetime - time.Minute),
				Subjects:   map[string]coreent.SubjectValue{},
				CIAccounts: tt.accounts,
			})
			url, bearer := "", ""
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "runner-secret"
			}
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", bearer)

			token := fixture.mintToken(t, now, tt.tokenOwner)
			mint := func(string, string) (resp *http.Response, err error) {
				body := `{"value":"` + token + `"}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			}

			//: An empty key directory, so any success can only have come from
			//: the CI path — a device would have been found otherwise.
			svc := svcent.NewServiceWithOrigins(
				getterFunc(fixture.get), entitlement.NewSSHIdentity(t.TempDir()), fixture.vendor,
				[]coreent.OriginValue{{Name: "test", BundleURL: "https://example.test/roster.signed.json"}},
			).WithBearerFetch(mint)

			grant, err := svc.Verify(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
			if grant.Subject != tt.wantSubject {
				t.Errorf("Verify() subject = %q, want %q", grant.Subject, tt.wantSubject)
			}
			//: The deadline must be bounded by the roster, exactly as a device
			//: grant is: a CI seat may not outlive what authorised it.
			if grant.NotAfter.IsZero() {
				t.Error("Verify() returned a CI grant with no deadline")
			}
		})
	}
}

// getterFunc adapts a function to the svcent.Getter the svcent.Service fetches through.
type getterFunc func(url string) (*http.Response, error)

// Get retrieves a URL.
func (f getterFunc) Get(url string) (resp *http.Response, err error) {
	return f(url)
}

// TestService_WithBearerFetch pins that the transport can be replaced, which
// is what lets the CI path be exercised without a network.
//
// It goes through a full Verify against signed fixtures rather than calling
// the mint directly: reaching the transport at all requires the roster to
// authenticate and to carry a CI block first, and that ordering is part of
// what this asserts.
func TestService_WithBearerFetch(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// accounts is what the roster grants; without a CI block the mint is
		// never reached, which is itself worth pinning.
		accounts map[string]coreent.CIEntitlementValue
		want     bool
		reason   string
	}{
		{
			name:     "a replaced transport is used when the roster grants CI",
			accounts: map[string]coreent.CIEntitlementValue{ciAccountID: {}},
			want:     true,
			reason:   "a test must be able to answer the mint request itself",
		},
		{
			name:     "no CI block means the mint is never attempted",
			accounts: nil,
			want:     false,
			reason:   "spending round trips to reach a refusal already known is waste",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSeatFixture(t, coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(coreent.RosterLifetime - time.Minute),
				Subjects:   map[string]coreent.SubjectValue{},
				CIAccounts: tt.accounts,
			})
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://x.actions.githubusercontent.com/token")
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "secret")

			called := false
			token := fixture.mintToken(t, now, ciAccountID)
			svc := svcent.NewServiceWithOrigins(
				getterFunc(fixture.get), entitlement.NewSSHIdentity(t.TempDir()), fixture.vendor,
				[]coreent.OriginValue{{Name: "test", BundleURL: "https://example.test/roster.signed.json"}},
			).WithBearerFetch(func(string, string) (resp *http.Response, err error) {
				called = true
				body := `{"value":"` + token + `"}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})

			//: The outcome is asserted through `called`; a refusal here is expected
			//: in the case that never reaches the mint at all.
			if _, verifyErr := svc.Verify(now); verifyErr != nil {
				t.Logf("Verify() refused, as this case may: %v", verifyErr)
			}

			if called != tt.want {
				t.Errorf("replaced transport called = %v, want %v (%s)", called, tt.want, tt.reason)
			}
		})
	}
}

// TestVerifyRequiresTheCISeatInsideActions pins the inversion: inside GitHub
// Actions the CI seat is REQUIRED, and only the signed roster can relax it.
//
// The hole it closes: a CI-seat failure used to fall through unconditionally,
// so one device key dropped onto a runner bought unlimited CI with no CI
// entitlement at all. For a licence scheme whose point is to meter CI, that was
// not a corner case — it was the way around the whole mechanism.
//
// The rows are chosen so the fallback is not merely unused but visibly
// PREVENTED: the key directory holds a fully valid, listed, possession-proving
// identity in every case. Without the strict default the device path authorises
// the first two and the test cannot fail.
//
// Not parallel: t.Setenv and t.Parallel panic together, and the runner
// variables are what InCI reads.
func TestVerifyRequiresTheCISeatInsideActions(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// relaxed is what the vendor signed: it restores the fall-through.
		relaxed bool
		// inCI controls whether the runner variables are present.
		inCI bool
		// accounts is what the roster grants CI.
		accounts map[string]coreent.CIEntitlementValue
		wantErr  error
		reason   string
	}{
		{
			name:     "an uncovered CI run is refused even holding a device key",
			inCI:     true,
			accounts: map[string]coreent.CIEntitlementValue{"999": {}},
			wantErr:  coreent.ErrCINotEntitled,
			reason:   "this is the whole point: the device key must not silently stand in for the seat",
		},
		{
			name:     "a roster with no CI block refuses every CI run",
			inCI:     true,
			accounts: nil,
			wantErr:  coreent.ErrCINotEntitled,
			reason:   "publishing a `ci` block is the deployment step this inversion requires, and its absence fails loudly rather than quietly opening the hole",
		},
		{
			name:     "a relaxed roster restores the fall-through",
			relaxed:  true,
			inCI:     true,
			accounts: map[string]coreent.CIEntitlementValue{"999": {}},
			wantErr:  nil,
			reason:   "a self-hosted runner carrying a device key is a real deployment; only the vendor may bless it, and this row is what proves they still can",
		},
		{
			name:     "a laptop is left alone",
			inCI:     false,
			accounts: map[string]coreent.CIEntitlementValue{"999": {}},
			wantErr:  nil,
			reason:   "InCI gates the strictness; without it the inversion would refuse every device in the field, none of which ever had a seat to prove",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, bearer := "", ""
			//: Both variables, or neither: InCI requires the pair.
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "runner-secret"
			}
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", bearer)

			dir := t.TempDir()
			//: A COMPLETE, listed, possession-proving identity. It is what
			//: makes a false negative impossible: the refusing rows would be
			//: authorised by the device path if the strict default did nothing.
			fingerprint := enrol(t, dir, sampleUUID)

			//: A real key set and a real RS256 token, so the seat fails on
			//: ENTITLEMENT rather than on a key set the stub could not serve.
			fixture := newSeatFixture(t, coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(time.Hour),
				Subjects:   map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
				CIAccounts: tt.accounts,
				CIRelaxed:  tt.relaxed,
			})
			token := fixture.mintToken(t, now, ciAccountID)
			mint := func(string, string) (resp *http.Response, err error) {
				//: A genuine, correctly signed token for an account the
				//: roster does not list.
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"value":"` + token + `"}`)),
				}, nil
			}

			svc := svcent.NewServiceWithOrigins(
				getterFunc(fixture.get), entitlement.NewSSHIdentity(dir), fixture.vendor, testOrigins("solo"),
			).WithBearerFetch(mint)

			_, err := svc.Verify(now)
			//: The authorised rows must authorise: refusing a laptop would be
			//: a far worse defect than the one this test closes.
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify() error = %v, want nil (%s)", err, tt.reason)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
			}
			//: The refusal must be the CI one and NOT a device one: reporting
			//: "no licence key found" on a runner sends an operator to
			//: `license create` on a machine that will never hold a key.
			if errors.Is(err, coreent.ErrNoLicense) {
				t.Errorf("Verify() error = %v, want no coreent.ErrNoLicense in it (%s)", err, tt.reason)
			}
		})
	}
}

// TestVerifyNamesTheCISeatFailureAlongsideTheDeviceOne pins that a refusal on
// a runner says BOTH halves in its MESSAGE — and that the seat's error chain
// does not come with it.
//
// The seat is tried first and every failure there is swallowed, which is
// correct right up to the moment the device path also refuses. An operator was
// then told "no licence key found" — the answer for a laptop, and the wrong
// place entirely to send somebody whose runner is simply not covered.
//
// The chain is checked NEGATIVELY on purpose. ciContext folds the seat cause in
// with %v, because the seat's chain is not CI-only: a JWKS outage carries
// coreent.ErrRosterUnreachable, which both licenseExitCode and licenseAdvice match
// ahead of several device sentinels. Test_ciContext pins that hazard directly;
// this test pins that Verify's own output has the same property end to end.
//
// Not parallel: t.Setenv.
func TestVerifyNamesTheCISeatFailureAlongsideTheDeviceOne(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		inCI bool
		// wantCIText is whether the seat failure must appear in the message.
		wantCIText bool
		reason     string
	}{
		{name: "inside CI both halves are named", inCI: true, wantCIText: true, reason: "the seat failure is the actionable half on a runner"},
		{name: "outside CI nothing is added", inCI: false, wantCIText: false, reason: "\"not running in GitHub Actions\" is noise on a laptop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, bearer := "", ""
			//: Both variables, or neither: InCI requires the pair.
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "runner-secret"
			}
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", bearer)

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}
			//: An EMPTY key directory, so the device half refuses with
			//: coreent.ErrNoLicense — which is exactly the misleading message.
			//:
			//: RELAXED, because that is now the only shape on which a device
			//: refusal is still what gets reported inside Actions: under the
			//: strict default the CI refusal is final and there is no device
			//: half to annotate. The annotation exists precisely for this
			//: deployment, so this is where it has to be tested.
			bundle := signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(time.Hour),
				Subjects:   map[string]coreent.SubjectValue{},
				CIAccounts: map[string]coreent.CIEntitlementValue{"999": {}},
				CIRelaxed:  true,
			})

			//: An empty key directory used to produce this; the identity now
			//: reports it directly, which says what the case is about instead
			//: of arranging a filesystem that implies it.
			svc := svcent.NewServiceWithOrigins(
				&stubGetter{bundle: bundle},
				stubIdentity{discoverErr: coreent.ErrNoLicense},
				vendorPub, testOrigins("solo"),
			).WithBearerFetch(refusingMint)

			_, err := svc.Verify(now)
			//: The device sentinel must survive the annotation, because the
			//: exit-code dispatch and the advice table are both built on it.
			if !errors.Is(err, coreent.ErrNoLicense) {
				t.Fatalf("Verify() error = %v, want it to still wrap coreent.ErrNoLicense (%s)", err, tt.reason)
			}
			//: The seat cause reaches the TEXT...
			if got := strings.Contains(err.Error(), "CI seat was refused too"); got != tt.wantCIText {
				t.Errorf("message carries the seat cause = %v, want %v — err = %v (%s)", got, tt.wantCIText, err, tt.reason)
			}
			//: ...and never the chain. coreent.ErrCIUnverifiable reachable here would
			//: mean %w had been used, which is how a JWKS outage's
			//: coreent.ErrRosterUnreachable would reach the exit-code switch and
			//: outrank the device refusal it is attached to.
			if errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("errors.Is(err, coreent.ErrCIUnverifiable) = true, want false — the seat's chain must not be spliced in (%s)", tt.reason)
			}
		})
	}
}

// refusingMint answers the Actions token endpoint with a 403, the status a
// workflow without `id-token: write` actually receives. It keeps the CI branch
// failing for a reason that is neither a network error nor a forgery.
func refusingMint(_, _ string) (resp *http.Response, err error) {
	//: A refusal to mint, shaped exactly as the runner would return one.
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

// Test_Service_WithTimeServers pins that the clock corroboration is OFF unless
// somebody asked for it.
//
// Committed source pins no Roughtime server — the client has never completed a
// handshake with a live one — so the default has to be a list nobody consults.
// A svcent.Service that reached the network for time without being told to would put a
// UDP round trip on every cold start, on a port most locked-down networks drop.
func Test_Service_WithTimeServers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// servers is what the caller configures.
		servers []svcent.RoughtimeServerValue
		reason  string
	}{
		{name: "an empty list is the default and changes nothing", servers: nil, reason: "no server configured means no opinion about the clock"},
		{
			name:    "an unreachable server still authorises",
			servers: []svcent.RoughtimeServerValue{{Name: "stub", Address: "127.0.0.1:1", PublicKey: make([]byte, 32)}},
			reason:  "FAIL-OPEN is the whole safety of this feature: a firewall must never become a refused licence",
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

			now := time.Now()
			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			getter := &stubGetter{bundle: signPair(t, vendorPriv, coreent.RosterValue{
				IssuedAt:  now.Add(-time.Minute),
				ExpiresAt: now.Add(time.Hour),
				Subjects:  map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
			})}

			svc := svcent.NewServiceWithOrigins(getter, entitlement.NewSSHIdentity(dir), vendorPub, testOrigins("solo"))
			//: Chainable: the return must be the same svcent.Service, or a
			//: construction expression would silently drop the setting.
			if got := svc.WithTimeServers(tt.servers); got != svc {
				t.Fatalf("WithTimeServers() returned %p, want the receiver %p (%s)", got, svc, tt.reason)
			}

			if _, err := svc.Verify(now); err != nil {
				t.Errorf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}

// Test_Service_WithOrigins pins that WHERE the roster is fetched from changes
// nothing about whether the answer is believed.
//
// That is what makes the setter safe to expose at all — and it is not an
// abstract claim: the end-to-end suite redirects a linked binary with it, so if
// an origin carried any authority of its own, that suite would have introduced
// the very hole this branch exists to close.
func Test_Service_WithOrigins(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// impostor signs the roster with a key the verifier never saw.
		impostor bool
		wantErr  error
		reason   string
	}{
		{name: "a redirected verifier accepts a genuine roster", reason: "authority travels in the signature, so an origin is only ever an availability choice"},
		{name: "a redirected verifier refuses a forged one", impostor: true, wantErr: coreent.ErrRosterUnsigned, reason: "pointing a binary somewhere hostile gains that endpoint nothing — it can serve whatever it likes and still cannot forge the anchor"},
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

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			getter := &stubGetter{bundle: signPair(t, signWith, coreent.RosterValue{
				IssuedAt:  now.Add(-time.Minute),
				ExpiresAt: now.Add(time.Hour),
				Subjects:  map[string]coreent.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
			})}

			//: Built with the PRODUCTION origin list, then redirected — which
			//: is the shape the e2e binaries are in.
			svc := svcent.NewServiceWithGetter(getter, entitlement.NewSSHIdentity(dir), vendorPub, &testProduct)
			//: Chainable: the return must be the same svcent.Service, or a
			//: construction expression would silently drop the setting.
			if got := svc.WithOrigins(testOrigins("redirected")); got != svc {
				t.Fatalf("WithOrigins() returned %p, want the receiver %p (%s)", got, svc, tt.reason)
			}

			_, err := svc.Verify(now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Verify() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("Verify() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}
