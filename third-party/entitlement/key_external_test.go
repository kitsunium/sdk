package entitlement_test

import (
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
	"golang.org/x/crypto/ssh"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// ownerOnly is the permission a private key must carry; anything looser means
// the secret is machine-wide and the possession proof is theatre.
const ownerOnly os.FileMode = 0o600

// canonicalSubject is a syntactically valid UUID; validSubject refuses anything
// else before the file is ever read, which would short-circuit these tests.
const canonicalSubject string = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

// writeKeyPair drops an ed25519 pair into dir under uuid, in the layout the
// license command produces.
func writeKeyPair(t *testing.T, dir, uuid string, mode os.FileMode) (pub ssh.PublicKey, signer ssh.Signer) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling private key: %v", err)
	}
	if writeErr := os.WriteFile(entitlement.PrivateKeyPath(dir, uuid), pem.EncodeToMemory(block), mode); writeErr != nil {
		t.Fatalf("writing private key: %v", writeErr)
	}
	signer, err = ssh.NewSignerFromKey(priv)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("building signer: %v", err)
	}
	pub = signer.PublicKey()
	if writeErr := os.WriteFile(entitlement.PublicKeyPath(dir, uuid), ssh.MarshalAuthorizedKey(pub), 0o644); writeErr != nil {
		t.Fatalf("writing public key: %v", writeErr)
	}
	//: Return both halves so a test can prove or fail possession.
	return pub, signer
}

// TestPublicKeyPath pins that the UUID alone addresses the published half,
// which is what lets the roster hold no index.
func TestPublicKeyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		uuid string
		want string
	}{
		{name: "plain uuid", dir: "/home/u/.ssh", uuid: "abc", want: "/home/u/.ssh/abc.pub"},
		{name: "nested dir", dir: "/a/b", uuid: "x-1", want: "/a/b/x-1.pub"},
		{name: "empty dir stays relative", dir: "", uuid: "id", want: "id.pub"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := entitlement.PublicKeyPath(tt.dir, tt.uuid); got != filepath.Clean(tt.want) {
				t.Errorf("entitlement.PublicKeyPath(%q, %q) = %q, want %q", tt.dir, tt.uuid, got, tt.want)
			}
		})
	}
}

// TestPrivateKeyPath pins that the private half sits beside the public one
// under the same UUID name.
func TestPrivateKeyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		uuid string
		want string
	}{
		{name: "plain uuid", dir: "/home/u/.ssh", uuid: "abc", want: "/home/u/.ssh/abc"},
		{name: "nested dir", dir: "/a/b", uuid: "x-1", want: "/a/b/x-1"},
		{name: "empty dir stays relative", dir: "", uuid: "id", want: "id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := entitlement.PrivateKeyPath(tt.dir, tt.uuid); got != filepath.Clean(tt.want) {
				t.Errorf("entitlement.PrivateKeyPath(%q, %q) = %q, want %q", tt.dir, tt.uuid, got, tt.want)
			}
		})
	}
}

// TestProvePossession pins the property that makes publishing the public keys
// safe: holding the published half is never enough.
func TestProvePossession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		foreign bool
		wantErr error
	}{
		{name: "the matching private half proves possession", foreign: false},
		{name: "a different key cannot answer for this identity", foreign: true, wantErr: coreent.ErrKeyMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pub, signer := writeKeyPair(t, t.TempDir(), "aaaaaaaa-1111-4111-8111-111111111111", ownerOnly)
			//: Copying a published .pub and signing with your own key is
			//: exactly the attack a public roster invites.
			if tt.foreign {
				_, signer = writeKeyPair(t, t.TempDir(), "bbbbbbbb-2222-4222-8222-222222222222", ownerOnly)
			}

			err := entitlement.ProvePossession(signer, pub)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("entitlement.ProvePossession() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("entitlement.ProvePossession() error = %v, want nil", err)
			}
		})
	}
}

// TestSignerFromFile covers enrolment state.
//
// The permission guard is asserted per platform instead of here, because it
// cannot be stated once: key_unix_external_test.go pins that a loose key is
// refused, key_windows_external_test.go pins that the synthesised mode is
// ignored. Keeping a shared case meant skipping it on Windows, and this
// project forbids t.Skip — a test that does not run is not a test.
func TestSignerFromFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    os.FileMode
		absent  bool
		corrupt bool
		wantErr error
	}{
		{name: "an owner-only key loads", mode: ownerOnly},
		{name: "a missing key is the never-enrolled case", absent: true, wantErr: coreent.ErrNoLicense},
		{name: "a corrupt key is refused, not treated as absent", corrupt: true, wantErr: coreent.ErrNoPossession},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			uuid := "aaaaaaaa-1111-4111-8111-111111111111"
			//: An absent key must not be manufactured by the fixture.
			switch {
			case tt.absent:
				uuid = "cccccccc-9999-4999-8999-999999999999"
			case tt.corrupt:
				if err := os.WriteFile(entitlement.PrivateKeyPath(dir, uuid), []byte("not a key"), ownerOnly); err != nil {
					t.Fatalf("writing junk: %v", err)
				}
			default:
				writeKeyPair(t, dir, uuid, tt.mode)
			}

			_, err := entitlement.SignerFromFile(dir, uuid)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("entitlement.SignerFromFile() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("entitlement.SignerFromFile() error = %v, want nil", err)
			}
		})
	}
}

// TestLoadPublicKey covers enrolment state on the published half.
func TestLoadPublicKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		absent  bool
		wantErr error
	}{
		{name: "an enrolled key loads"},
		{name: "a missing key is the never-enrolled case", absent: true, wantErr: coreent.ErrNoLicense},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeKeyPair(t, dir, "eeeeeeee-5555-4555-8555-555555555555", ownerOnly)
			uuid := "eeeeeeee-5555-4555-8555-555555555555"
			//: Point at a key that was deliberately never written.
			if tt.absent {
				uuid = "dddddddd-0000-4000-8000-000000000000"
			}

			loaded, err := entitlement.LoadPublicKey(dir, uuid)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("entitlement.LoadPublicKey() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("entitlement.LoadPublicKey() error = %v, want nil", err)
			}
			if loaded == nil {
				t.Error("entitlement.LoadPublicKey() returned no key on the success path")
			}
		})
	}
}

// TestFingerprint pins that the value compared against the roster is stable
// across a write/read round trip — a mismatch here would revoke everyone.
func TestFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uuid string
	}{
		{name: "round trip is stable", uuid: "eeeeeeee-5555-4555-8555-555555555555"},
		{name: "a second identity fingerprints differently", uuid: "bbbbbbbb-2222-4222-8222-222222222222"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			pub, _ := writeKeyPair(t, dir, tt.uuid, ownerOnly)
			loaded, err := entitlement.LoadPublicKey(dir, tt.uuid)
			if err != nil {
				t.Fatalf("entitlement.LoadPublicKey() error = %v, want nil", err)
			}
			if entitlement.Fingerprint(loaded) != entitlement.Fingerprint(pub) {
				t.Errorf("entitlement.Fingerprint() = %q, want %q", entitlement.Fingerprint(loaded), entitlement.Fingerprint(pub))
			}
		})
	}
}

// TestSubjectValidation pins that an identifier which could escape the key
// directory is refused before any path is composed. The subject reaches us
// from a roster and from directory listings, so treating it as a trusted path
// component would turn the licence name into a traversal lever.
func TestSubjectValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uuid    string
		wantErr error
	}{
		{name: "parent traversal is refused", uuid: "../../etc/passwd", wantErr: coreent.ErrKeyMismatch},
		{name: "a nested path is refused", uuid: "a/b", wantErr: coreent.ErrKeyMismatch},
		{name: "an absolute path is refused", uuid: "/etc/shadow", wantErr: coreent.ErrKeyMismatch},
		{name: "a plain word is not a subject", uuid: "id_ed25519", wantErr: coreent.ErrKeyMismatch},
		{name: "an empty identifier is refused", uuid: "", wantErr: coreent.ErrKeyMismatch},
		{name: "a canonical subject is accepted as far as the filesystem", uuid: sampleUUID, wantErr: coreent.ErrNoLicense},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			//: Both entry points compose a path from the identifier, so both
			//: must reject before touching the filesystem.
			if _, err := entitlement.LoadPublicKey(dir, tt.uuid); !errors.Is(err, tt.wantErr) {
				t.Errorf("entitlement.LoadPublicKey(%q) error = %v, want %v", tt.uuid, err, tt.wantErr)
			}
			if _, err := entitlement.SignerFromFile(dir, tt.uuid); !errors.Is(err, tt.wantErr) {
				t.Errorf("entitlement.SignerFromFile(%q) error = %v, want %v", tt.uuid, err, tt.wantErr)
			}
		})
	}
}

// probeReadCause asks the operating system what os.ReadFile produces for path,
// and returns the syscall-level cause underneath its *fs.PathError.
//
// Naming the errno directly would bind the test to one platform. Probing binds
// it to the property under test — that entitlement.LoadPublicKey preserves whatever cause
// the read produced — which is true everywhere and is what the fix restored.
//
// Parameters:
//   - t: the test handle, used to fail on a fixture that does not error.
//   - path: the path to read.
//
// Returns:
//   - cause: the unwrapped cause, suitable as an errors.Is target.
func probeReadCause(t *testing.T, path string) (cause error) {
	t.Helper()

	_, probeErr := os.ReadFile(path)
	//: A fixture that reads cleanly cannot exercise the wrapping at all.
	if probeErr == nil {
		t.Fatalf("reading %s succeeded; the fixture must make ReadFile fail", path)
	}

	var pathErr *fs.PathError
	//: os.ReadFile always reports through *fs.PathError; anything else means
	//: the probe and the code under test are not on the same path.
	if !errors.As(probeErr, &pathErr) {
		t.Fatalf("probe error is not a *fs.PathError: %v", probeErr)
	}
	//: Return the syscall-level cause, which is what entitlement.LoadPublicKey must carry.
	return pathErr.Err
}

// TestLoadPublicKeyPreservesTheUnderlyingCause pins that this package no longer
// flattens the real cause out of its error chains.
//
// Every wrap in pkg/license used the shape
//
//	fmt.Errorf("%w: context: %v", ErrSentinel, cause)
//
// which wraps the SENTINEL and renders the CAUSE as text. errors.Is against the
// sentinel worked, so nothing looked broken — but errors.Is against the actual
// cause could never succeed, and the repository contains zero production
// errors.As, which is what you would expect when As can never return anything.
//
// Go 1.20 made multiple %w legal in one Errorf, so both can be wrapped. This
// test is what keeps it fixed: the sentinel must STILL match, because callers
// branch on it, and the underlying cause must match too.
//
// The fixture is a DIRECTORY where a key file is expected. That reaches the
// "reading" branch rather than the absent-file branch, which returns a
// different sentinel and deliberately wraps no cause.
//
// The expected cause is PROBED rather than named. On unix, reading a directory
// fails with EISDIR; on windows the open succeeds (Go opens directories with
// FILE_FLAG_BACKUP_SEMANTICS) and the read fails with something else entirely,
// so a hardcoded syscall.EISDIR would have made the [windows] pkg/license CI
// lane red for a platform difference this test is not about. Asking the OS what
// it produces keeps the assertion the one that matters: whatever the cause IS,
// it must be reachable through the chain.
func TestLoadPublicKeyPreservesTheUnderlyingCause(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	keyPath := filepath.Join(sshDir, canonicalSubject+".pub")
	//: A directory at the key path makes ReadFile fail.
	if err := os.Mkdir(keyPath, 0o750); err != nil {
		t.Fatalf("preparing fixture: %v", err)
	}

	wantCause := probeReadCause(t, keyPath)

	_, err := entitlement.LoadPublicKey(sshDir, canonicalSubject)
	//: A nil error would make every assertion below vacuous.
	if err == nil {
		t.Fatal("entitlement.LoadPublicKey over a directory returned nil error")
	}

	tests := []struct {
		name   string
		target error
		want   bool
		why    string
	}{
		{
			name:   "sentinel still matches",
			target: coreent.ErrNoPossession,
			want:   true,
			why:    "callers branch on the sentinel; that contract must not change",
		},
		{
			name:   "underlying cause now matches",
			target: wantCause,
			want:   true,
			why:    "the %v that flattened this cause is now a %w",
		},
		{
			name:   "an unrelated error still does not match",
			target: fs.ErrPermission,
			want:   false,
			why:    "wrapping more must not make errors.Is promiscuous",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: errors.Is must see the whole chain, not just its head.
			if got := errors.Is(err, tt.target); got != tt.want {
				t.Errorf("errors.Is(err, %v) = %v, want %v (%s)\nerr = %v", tt.target, got, tt.want, tt.why, err)
			}
		})
	}
}

// TestErrorsAsReachesTheConcreteCause is the other half: errors.As must recover
// the concrete error type, which is impossible once a cause has been rendered
// to text.
//
// The negative row is what keeps the positive one honest. `errors.As` returning
// true for *fs.PathError proves the chain reaches a wrapped value; it does not
// prove the chain is DISCRIMINATING. A wrapper that answered every As query
// affirmatively would satisfy the first row and be worse than the flattening it
// replaced, because callers would branch on types the error never carried.
func TestErrorsAsReachesTheConcreteCause(t *testing.T) {
	t.Parallel()

	sshDir := t.TempDir()
	keyPath := filepath.Join(sshDir, canonicalSubject+".pub")
	//: Same fixture: a directory where a file is expected.
	if err := os.Mkdir(keyPath, 0o750); err != nil {
		t.Fatalf("preparing fixture: %v", err)
	}

	_, err := entitlement.LoadPublicKey(sshDir, canonicalSubject)
	//: Without an error there is nothing to unwrap.
	if err == nil {
		t.Fatal("entitlement.LoadPublicKey over a directory returned nil error")
	}

	tests := []struct {
		name     string
		recovers func(error) bool
		want     bool
		why      string
	}{
		{
			name: "the concrete *fs.PathError is recovered",
			recovers: func(e error) bool {
				var target *fs.PathError
				return errors.As(e, &target)
			},
			want: true,
			why:  "a cause rendered with %v is text; As can never reach it at any depth",
		},
		{
			name: "an unrelated concrete type is not",
			recovers: func(e error) bool {
				var target *os.LinkError
				return errors.As(e, &target)
			},
			want: false,
			why:  "wrapping the cause must not make As promiscuous",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: As must reach the real value, and only the real value.
			if got := tt.recovers(err); got != tt.want {
				t.Errorf("errors.As(%s) = %v, want %v (%s)\nerr = %v", tt.name, got, tt.want, tt.why, err)
			}
		})
	}

	var pathErr *fs.PathError
	//: A flattened cause cannot be recovered by As at any depth.
	if !errors.As(err, &pathErr) {
		t.Fatalf("errors.As could not recover *fs.PathError from: %v", err)
	}
	//: The recovered error must carry the real operand, not a copy of the text.
	if pathErr.Path != keyPath {
		t.Errorf("recovered *fs.PathError.Path = %q, want %q", pathErr.Path, keyPath)
	}
}
