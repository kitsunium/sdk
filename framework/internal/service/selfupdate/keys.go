package selfupdate

import (
	"crypto/ed25519"
	"log"
	"strings"
	"time"

	coreupd "github.com/kitsunium/sdk/framework/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/crypto/sign"
)

// maxVendorKeys bounds the key list, as ADR 0091 bounds the entitlement
// anchors: two for a rotation, three for an interrupted one, four with
// headroom. Past it the tail is dropped and logged — a build mistake answered
// by refusing every update would strand every installation.
const maxVendorKeys int = 4

// The signed statement's header lines, when a signature domain is set.
const (
	statementTag     string = "# tag "
	statementExpires string = "# expires "
)

// WithVendorKeys links the keys a release may be signed with, in order: a
// release verifies when ANY of them verifies its manifest (ADR 0150). A key
// rotation publishes under the new key while builds carrying both accept it
// and builds carrying only the old one keep reading the old — neither side has
// to be updated first. Several keys are still ONE signer: no quorum is taken.
// A key of the wrong length is kept as it is and refused at verification,
// naming its length, exactly as a single wrong key always was.
func (u *Service) WithVendorKeys(keys ...[]byte) *Service {
	if u == nil {
		return nil
	}
	if len(keys) > maxVendorKeys {
		log.Printf("selfupdate: %d vendor keys linked, %d kept: the tail is dropped", len(keys), maxVendorKeys)
		keys = keys[:maxVendorKeys]
	}
	u.vendorKeys = u.vendorKeys[:0]
	for _, k := range keys {
		u.vendorKeys = append(u.vendorKeys, ed25519.PublicKey(k))
	}
	u.vendorKey = nil
	if len(u.vendorKeys) > 0 {
		u.vendorKey = u.vendorKeys[0]
	}
	return u
}

// WithSignatureDomain makes every signature cover domain, a NUL byte and the
// manifest — never the manifest alone — so a key the vendor also signs other
// documents with (a roster, another product's releases) cannot have one of
// those signatures read as a release of this product. It also requires the
// manifest to say which release it is and until when it may be installed, in
// two header lines the signature therefore covers:
//
//	# tag v1.4.0
//	# expires 2026-12-31T00:00:00Z
//
// A manifest naming another tag is refused — the replay of an older, signed
// release under a newer name —, and so is one past its expiry — the freeze an
// attacker who can only replay can otherwise hold an installation in. Without
// a domain the historical form is verified unchanged.
func (u *Service) WithSignatureDomain(domain string) *Service {
	if u == nil {
		return nil
	}
	u.domain = domain
	return u
}

// verifiedByAnyKey reports whether one of the linked keys verifies signature
// over message.
//
// Each key is asked through the crypto domain's signing facade (ADR 0158 §2),
// the verifier the rest of the SDK uses. The facade answers a key or a
// signature of the wrong length with false, and an error only for an algorithm
// it does not register — which Ed25519 always is, since importing the facade
// registers it — so an error counts as "this key did not verify".
func (u *Service) verifiedByAnyKey(message, signature []byte) bool {
	for _, k := range u.keys() {
		if len(k) != ed25519.PublicKeySize {
			continue
		}
		if verified, verifyErr := sign.Verify(sign.Ed25519, k, message, signature); verifyErr == nil && verified {
			return true
		}
	}
	return false
}

// keys is the linked list, or the single key a Service was given before the
// list existed.
func (u *Service) keys() []ed25519.PublicKey {
	if len(u.vendorKeys) == 0 && u.vendorKey != nil {
		return []ed25519.PublicKey{u.vendorKey}
	}
	return u.vendorKeys
}

// signedMessage is what a signature covers: the manifest, or — with a
// domain — the domain, a NUL byte and the manifest.
func (u *Service) signedMessage(manifest string) string {
	if u.domain == "" {
		return manifest
	}
	return u.domain + "\x00" + manifest
}

// checkStatement reads, from a manifest the signature already vouched for, the
// tag and the expiry a domain requires, and refuses a manifest that lacks
// either, names another tag, or has expired.
func (u *Service) checkStatement(tag, manifest string) error {
	if u.domain == "" {
		return nil
	}
	var signedTag, expires string
	for line := range strings.SplitSeq(manifest, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, statementTag):
			signedTag = strings.TrimSpace(strings.TrimPrefix(line, statementTag))
		case strings.HasPrefix(line, statementExpires):
			expires = strings.TrimSpace(strings.TrimPrefix(line, statementExpires))
		}
	}
	if signedTag == "" || expires == "" {
		return refuse(coreupd.SignatureInvalid, errs.String("condition", "statement_incomplete"), errs.String("tag", tag))
	}
	if normalizeVersion(signedTag) != normalizeVersion(tag) {
		return refuse(coreupd.SignatureInvalid, errs.String("condition", "tag_mismatch"),
			errs.String("tag", tag), errs.String("signed_tag", signedTag))
	}
	until, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return refuse(coreupd.SignatureInvalid, errs.String("condition", "expiry_unreadable"), errs.String("tag", tag))
	}
	//: Read against the injected clock, the system one when none was given:
	//: an expiry is a deadline, and a deadline a suite cannot move is one it
	//: cannot test from both sides.
	reader := u.clock
	if reader == nil {
		reader = clock.System
	}
	if !reader.Now().Before(until) {
		return refuse(coreupd.SignatureInvalid, errs.String("condition", "statement_expired"),
			errs.String("tag", tag), errs.String("expired", until.UTC().Format(time.RFC3339)))
	}
	return nil
}
