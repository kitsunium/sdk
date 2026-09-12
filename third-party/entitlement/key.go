// Package entitlement - local key material and the possession proof. The public
// halves are published by design, so reading one proves nothing; only a
// fresh signature over a per-call nonce establishes ownership.
package entitlement

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/crypto/ssh"
)

// nonceSize is the length of the challenge signed to prove possession. 32
// bytes of entropy makes a precomputed signature table pointless.
const nonceSize int = 32

// keyFileMode keeps a private key readable only by its owner; anything looser
// means the "private" half is not private and the proof below is theatre.
const keyFileMode os.FileMode = 0o600

// subjectPattern is the canonical v4 UUID shape a subject identifier must
// have. Declared here rather than in service.go because every filesystem
// entry point validates against it before composing a path.
var subjectPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validSubject reports whether a subject identifier is a canonical v4 UUID.
//
// The identifier reaches us from the roster and from directory listings, and
// it is concatenated into a path. Anything but the exact canonical shape —
// a separator, a parent reference, an absolute prefix — must be refused
// before it ever touches the filesystem, otherwise the subject name becomes
// a path-traversal lever.
//
// Generation moved to stdlib uuid.NewV4; validation deliberately did not move
// to uuid.Parse. Parse accepts four renderings of the same value — dashed,
// undashed 32 hex, brace-wrapped, and urn:uuid: prefixed — and checks no
// version nibble. Three of those four are not the filename this package
// writes, so accepting them would let two spellings of one subject address
// two different files, and the urn: form carries a colon into a path. The
// regexp stays the narrower gate on purpose: what we mint is a subset of
// what Parse would take, and the gate matches what we mint.
func validSubject(uuid string) bool {
	//: Callers must refuse rather than sanitise: a subject that is not a
	//: canonical UUID is not a subject at all.
	return subjectPattern.MatchString(uuid)
}

// PublicKeyPath returns where a subject's public half lives. The file is named
// after the UUID so the lookup needs no index: the identifier IS the address.
func PublicKeyPath(sshDir, uuid string) string {
	//: Callers need the exact path to read or to report in diagnostics.
	return filepath.Join(sshDir, uuid+".pub")
}

// PrivateKeyPath returns where a subject's private half lives, alongside the
// public one and under the same UUID name.
func PrivateKeyPath(sshDir, uuid string) string {
	//: Callers need the exact path to read or to report in diagnostics.
	return filepath.Join(sshDir, uuid)
}

// LoadPublicKey reads and parses the local public half for a subject.
func LoadPublicKey(sshDir, uuid string) (pub ssh.PublicKey, err error) {
	//: Validate before composing a path: the identifier is untrusted input.
	if !validSubject(uuid) {
		//: Refuse a subject that could escape the key directory.
		return nil, fmt.Errorf("%w: %q is not a canonical subject", ErrKeyMismatch, uuid)
	}
	path := PublicKeyPath(sshDir, uuid)
	raw, readErr := os.ReadFile(path)
	//: Absence and inaccessibility are different operator problems: one
	//: needs enrolment, the other needs a chmod. Collapsing them would send
	//: someone chasing the wrong fix.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: Never enrolled, or the key was removed.
		return nil, fmt.Errorf("%w: %s", ErrNoLicense, path)
	}
	//: Any other read failure is an access problem, not an absent licence.
	if readErr != nil {
		//: Report the access failure with its cause.
		return nil, fmt.Errorf("%w: reading %s: %w", ErrNoPossession, path, readErr)
	}
	parsed, _, _, _, parseErr := ssh.ParseAuthorizedKey(raw)
	//: A key we cannot parse cannot be compared to the roster.
	if parseErr != nil {
		//: Refuse rather than authorize on an unreadable identity.
		return nil, fmt.Errorf("%w: parsing %s: %w", ErrKeyMismatch, path, parseErr)
	}
	//: Return the parsed key so the caller can fingerprint it.
	return parsed, nil
}

// Fingerprint renders the SHA256 fingerprint the roster records. Comparing
// fingerprints rather than raw bytes keeps the roster small and the check
// exact.
func Fingerprint(pub ssh.PublicKey) string {
	//: Callers compare this against the roster entry.
	return ssh.FingerprintSHA256(pub)
}

// ProvePossession verifies that signer controls the private half matching pub.
//
// This is the step that makes publishing the public keys safe. The roster is
// world-readable by design, so anyone can copy a .pub and claim the matching
// UUID; only the holder of the private half can sign a fresh challenge. The
// nonce is generated per call, so a captured signature is worthless.
func ProvePossession(signer ssh.Signer, pub ssh.PublicKey) error {
	//: A signer whose public half differs is answering for another identity.
	if signer.PublicKey().Type() != pub.Type() ||
		!bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		//: Answering with another identity's key is the copied-.pub attack.
		return fmt.Errorf("%w: signer is not the published key", ErrKeyMismatch)
	}

	var nonce [nonceSize]byte
	//: Without entropy the challenge is predictable and proves nothing.
	if _, err := rand.Read(nonce[:]); err != nil {
		//: Refuse rather than sign a guessable challenge.
		return fmt.Errorf("%w: drawing challenge: %w", ErrNoPossession, err)
	}

	sig, err := signer.Sign(rand.Reader, nonce[:])
	//: An agent that refuses to sign (locked, key removed) proves nothing.
	if err != nil {
		//: A locked or emptied agent cannot establish possession.
		return fmt.Errorf("%w: %w", ErrNoPossession, err)
	}
	//: The signature must verify against the published half, not the signer's.
	if verifyErr := pub.Verify(nonce[:], sig); verifyErr != nil {
		//: Signature does not match the identity being claimed.
		return fmt.Errorf("%w: %w", ErrNoPossession, verifyErr)
	}
	//: Possession established for this call only.
	return nil
}

// SignerFromFile loads an unencrypted private key. Passphrase-protected keys
// deliberately fail here: the linter never prompts for a passphrase, they are
// meant to be exercised through ssh-agent instead.
func SignerFromFile(sshDir, uuid string) (signer ssh.Signer, err error) {
	//: Validate before composing a path: the identifier is untrusted input.
	if !validSubject(uuid) {
		//: Refuse a subject that could escape the key directory.
		return nil, fmt.Errorf("%w: %q is not a canonical subject", ErrKeyMismatch, uuid)
	}
	path := PrivateKeyPath(sshDir, uuid)
	info, statErr := os.Stat(path)
	//: Absence and inaccessibility are different operator problems: one
	//: needs enrolment, the other needs a chmod.
	if errors.Is(statErr, fs.ErrNotExist) {
		//: Never enrolled — no private half on this machine.
		return nil, fmt.Errorf("%w: %s", ErrNoLicense, path)
	}
	//: Any other stat failure is an access problem, not an absent licence.
	if statErr != nil {
		//: Report the access failure with its cause.
		return nil, fmt.Errorf("%w: stat %s: %w", ErrNoPossession, path, statErr)
	}
	//: A world-readable private key is not private. What "readable beyond
	//: its owner" MEANS is platform-specific, so the test lives in
	//: key_unix.go and key_windows.go: on Windows os.Stat synthesises a
	//: 0666/0444 mode that carries no access-control meaning, and applying
	//: the POSIX test to it refused every key the tool had just written.
	if modeErr := checkPrivateKeyMode(info, path); modeErr != nil {
		//: Refuse loudly rather than authorize on a machine-wide secret.
		return nil, modeErr
	}

	raw, readErr := os.ReadFile(path)
	//: The file existed a moment ago, so a failure here is an access
	//: problem rather than an absent licence.
	if readErr != nil {
		//: Report the access failure with its cause.
		return nil, fmt.Errorf("%w: reading %s: %w", ErrNoPossession, path, readErr)
	}
	parsed, parseErr := ssh.ParsePrivateKey(raw)
	//: Encrypted keys land here too: the linter never prompts, use ssh-agent.
	if parseErr != nil {
		//: No usable signer means no possession proof.
		return nil, fmt.Errorf("%w: %w", ErrNoPossession, parseErr)
	}
	//: Return a signer able to answer a challenge.
	return parsed, nil
}
