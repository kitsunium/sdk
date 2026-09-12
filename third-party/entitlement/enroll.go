// Package entitlement - enrolment: minting a subject identity locally and turning
// it into a request the vendor can act on. The private half never leaves the
// machine; what travels is the public half and the UUID naming it.
package entitlement

import (
	"crypto/ed25519"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"uuid"

	"golang.org/x/crypto/ssh"
)

// requestLabel marks a first enrolment; the workflow keys off it.
const requestLabel string = "license:request"

// updateLabel marks a key rotation for an existing subject.
const updateLabel string = "license:update"

// publicKeyFileMode is world-readable on purpose: the published half is
// handed to everyone by the roster, so protecting it locally would be
// theatre.
const publicKeyFileMode os.FileMode = 0o644

// sshDirMode is what ssh itself requires of a key directory. Creating it any
// looser would make the private key unusable by other tooling and would
// contradict the permission SignerFromFile enforces on the key itself.
const sshDirMode os.FileMode = 0o700

// NewSubjectID mints a version 4 identifier. It is drawn locally because the
// vendor never needs to allocate it: collisions are not a practical concern
// at 122 bits of entropy, and self-allocation keeps enrolment offline until
// the moment the user files the issue.
//
// The hand-rolled version this replaces set the same bits over the same 16
// bytes of crypto/rand and rendered the same canonical dashed form, so the
// identifiers are indistinguishable across the change. It returned an error
// only because it checked crypto/rand.Read, which since Go 1.24 cannot fail —
// it panics instead. That branch was already unreachable, and the test that
// asserted err == nil could not have failed either. Dropping the result is
// the visible half of the swap.
func NewSubjectID() string {
	//: The canonical dashed lowercase form is what the key filename carries
	//: and what subjectPattern accepts; see validSubject for why parsing on
	//: the way back in stays stricter than uuid.Parse.
	return uuid.NewV4().String()
}

// GenerateKeyPair writes a fresh ed25519 pair named after subject. ed25519
// over RSA is deliberate: shorter, faster, the current default in .ssh, and
// the same primitive the roster signature uses.
//
// The parameter is named subject rather than uuid because the stdlib package
// of that name is imported here.
func (p *ProductValue) GenerateKeyPair(sshDir, subject string) (publicKey string, err error) {
	//: Validate BEFORE building any path. Every other entry point into this
	//: file's key paths (LoadPublicKey, SignerFromFile) checks the subject
	//: first; this one did not, so a subject like "../authorized_keys" wrote
	//: both halves of a key pair outside sshDir.
	if !validSubject(subject) {
		//: Refuse rather than write anywhere the caller did not name.
		return "", fmt.Errorf("%w: subject %q is not a canonical v4 UUID", ErrNoLicense, subject)
	}

	//: A fresh machine, a container or a CI runner has no ~/.ssh yet, and
	//: failing there would make enrolment impossible in exactly the places
	//: that need it most. MkdirAll is a no-op when the directory exists, so
	//: an operator's stricter permissions are left untouched.
	if dirErr := os.MkdirAll(sshDir, sshDirMode); dirErr != nil {
		//: Without a key directory there is nowhere to enrol.
		return "", fmt.Errorf("creating key directory %s: %w", sshDir, dirErr)
	}

	pub, priv, genErr := ed25519.GenerateKey(nil)
	//: A key we cannot generate cannot be enrolled.
	if genErr != nil {
		//: Refuse rather than write half an identity.
		return "", fmt.Errorf("generating key: %w", genErr)
	}

	block, marshalErr := ssh.MarshalPrivateKey(priv, p.Name+" entitlement "+subject)
	//: A private half we cannot serialise is unusable.
	if marshalErr != nil {
		//: Refuse rather than write half an identity.
		return "", fmt.Errorf("marshalling private key: %w", marshalErr)
	}
	//: Write the private half first and owner-only: a later failure leaves a
	//: useless key rather than a published identity with no way to prove it.
	if writeErr := os.WriteFile(PrivateKeyPath(sshDir, subject), pem.EncodeToMemory(block), keyFileMode); writeErr != nil {
		//: Refuse rather than continue without a private half.
		return "", fmt.Errorf("writing private key: %w", writeErr)
	}

	sshPub, convErr := ssh.NewPublicKey(pub)
	//: A public half we cannot serialise cannot be published.
	if convErr != nil {
		//: Refuse rather than enrol an unverifiable identity.
		return "", fmt.Errorf("converting public key: %w", convErr)
	}
	authorized := ssh.MarshalAuthorizedKey(sshPub)
	//: The published half is world-readable by design; its secrecy was never
	//: part of the scheme.
	if writeErr := os.WriteFile(PublicKeyPath(sshDir, subject), authorized, publicKeyFileMode); writeErr != nil {
		//: Refuse rather than leave the pair half-written.
		return "", fmt.Errorf("writing public key: %w", writeErr)
	}
	//: Return the authorized-keys line the issue will carry.
	return string(authorized), nil
}

// IssueURL builds the prefilled enrolment request. The CLI never holds a
// GitHub token: it hands the user a link, and the account that opens the
// issue is the identity the workflow binds the UUID to.
func (p *ProductValue) IssueURL(subject, publicKey string, rotation bool) string {
	label := requestLabel
	//: A rotation must be distinguishable so the workflow can demand that
	//: the author already owns the subject.
	if rotation {
		label = updateLabel
	}
	action := "Enrol"
	//: Mirror the label in the title so the queue is readable at a glance.
	if rotation {
		action = "Rotate key for"
	}

	body := fmt.Sprintf(
		"### Subject\n\n%s\n\n### Public key\n\n```\n%s```\n\n"+
			"_The private half never left the machine._\n",
		subject, publicKey)

	query := url.Values{}
	query.Set("title", fmt.Sprintf("%s %s", action, subject))
	query.Set("labels", label)
	query.Set("body", body)
	//: Return a link the user can open without any credential of ours.
	return p.EnrolURL + "?" + query.Encode()
}
