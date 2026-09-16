// Package entitlement — the trust anchors this verifier accepts, and why there
// is more than one of them.
//
// # The rotation that had no in-band path
//
// One anchor is one key with no way out. An installation whose anchor must
// change — because the vendor's signing key is being retired, or because it was
// compromised — had no path in band: rosters signed by a new key B are refused
// against the anchor A the binary links in, BEFORE anything reads the version
// floor that would have told it to upgrade; and a release signed by B is refused
// by the very installation that needs it. Blocked from both sides, with the only
// remaining route out of band — an operator fetching a binary by hand.
//
// Accepting an ORDERED LIST of anchors is what opens that path: publish B, and
// the installations carrying {A, B} take it while those carrying only {A} keep
// reading A's rosters until they are updated. Neither side has to be updated
// first, which is the whole of what rotation needs.
//
// # The tension, stated rather than hidden
//
// Every anchor on the list is a key whose compromise is ACCEPTED for as long as
// it stays there. A list is therefore strictly more surface than a single key,
// and the mitigation is not a mechanism — it is that the list is ORDERED, BOUNDED
// at maxAnchors, and decided at BUILD time. Nothing at runtime can add to it: no
// roster field, no environment variable, no cache file. A compromised key leaves
// the list the way it entered — by shipping a build without it — and the bound is
// what keeps that a decision somebody has to make rather than a slot that is
// always free.
package entitlement

import (
	"errors"
	"log"
	"slices"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// maxAnchors bounds how many vendor keys one verifier will accept.
//
// A rotation needs two — the key being retired and the key replacing it — and a
// rotation interrupted by a second one needs three. Four leaves that headroom
// and stops there, because the list's cost is exactly its length: an anchor is a
// key whose compromise is accepted while it is listed, so a bound that is never
// reached is a bound that lets the list become the place retired keys accumulate.
const maxAnchors int = 4

// anchorList normalises what a build declares into the list a verifier holds.
//
// The slice is COPIED, so a caller that later appends to its own cannot extend
// the set a running verifier trusts. The key BYTES are shared, exactly as the
// single-key form has always shared them — deep-copying them would advertise an
// immutability this package cannot actually provide, since the caller keeps the
// backing array either way.
//
// Past maxAnchors it CLAMPS and logs rather than refusing, which is ADR 0031's
// clamp half and deliberate here: the list is a build decision, so an over-long
// one is a build mistake, and answering a build mistake by refusing every
// verification would take the product down over a misconfiguration that a log
// line reports precisely. The entries dropped are the LAST ones, which is the
// only end that can be dropped on a list whose order is its statement of
// preference.
func anchorList(anchors [][]byte) [][]byte {
	kept := anchors
	//: Past the bound, keep the head: the order is the build's statement of
	//: which anchor is current, so the tail is the only end droppable without
	//: overriding it.
	if len(kept) > maxAnchors {
		log.Printf("entitlement: %d trust anchors declared, keeping the first %d", len(kept), maxAnchors)
		kept = kept[:maxAnchors]
	}
	//: The list this verifier will try, in order. Cloned rather than held by
	//: reference: an append that fits the caller's spare capacity would
	//: otherwise write into the array this verifier reads.
	return slices.Clone(kept)
}

// parseBundleAnyAnchor authenticates a bundle against the first accepted anchor
// that verifies it, and applies the identical window rules afterwards.
//
// # Why any anchor is enough
//
// A signature made with any key on the list is a statement by the vendor, which
// is the only thing authentication establishes. Requiring more — a quorum, or a
// specific anchor per origin — would answer a question the scheme does not ask:
// there is one signer, and the list exists because that signer's KEY changes,
// never because several parties must agree.
//
// # Why the loop moves on from one failure and only one
//
// The same rule currentRoster applies to origins, for the same reason: move on
// from a document this anchor cannot AUTHENTICATE, never from one it
// authenticated and the rules then refused. coreent.ErrRosterUnsigned is exactly
// the first case — a malformed anchor, or a signature that does not verify
// against it — and every other refusal (an undecodable bundle, a duplicated
// member name, a window that is over-wide, unopened or closed) is a property of
// the DOCUMENT and identical under every anchor.
//
// Continuing past a document-level refusal would be a real defect and not merely
// noise: an expired roster tried against a second anchor comes back
// coreent.ErrRosterUnsigned, so an operator whose clock or whose publisher had
// simply fallen behind would be told somebody is impersonating the vendor.
func parseBundleAnyAnchor(raw []byte, anchors [][]byte, now time.Time) (roster *coreent.RosterValue, err error) {
	//: A build that linked in no anchor at all authorises nothing, and says so
	//: under the sentinel that already means "no valid vendor signature" — with
	//: no anchor, no signature can be one. A sixteenth code was refused for the
	//: reason core/entitlement's CLAUDE.md records: a caller mapping sentinels
	//: to advice lives downstream and a new code falls through to its default.
	if len(anchors) == 0 {
		//: Refuse with a cause a build can act on.
		return nil, refuse(coreent.ErrRosterUnsigned,
			errs.String("condition", "no trust anchor was linked into this build, so no signature can be valid"))
	}

	var lastErr error
	//: In order: the build's preference, tried as the build stated it.
	for _, anchor := range anchors {
		candidate, parseErr := ParseBundle(raw, anchor, now)
		//: Authentic under this anchor, and inside its window.
		if parseErr == nil {
			//: Authenticated.
			return candidate, nil
		}
		//: Not a question about the anchor, so no other anchor answers it
		//: differently — and reporting it as a forgery would be worse than
		//: useless. Propagate the document's own refusal.
		if !errors.Is(parseErr, coreent.ErrRosterUnsigned) {
			//: Propagate the refusal the document earned.
			return nil, parseErr
		}
		lastErr = parseErr
	}
	//: No accepted anchor authenticates these bytes; report the last attempt,
	//: which carries the spoofing sentinel every attempt carried.
	return nil, lastErr
}

// bundleMarkAnyAnchor reads what a signed bundle proves about time, against any
// accepted anchor.
//
// A bundle cached under an anchor that is still accepted is still the vendor's
// signed statement about when it was issued, so a rotation must not reset the
// ratchet: dropping the mark would hand back exactly the rollback distance the
// ratchet exists to hold, at the one moment — a key rotation — when an
// installation is least able to re-establish it.
//
// Both sides of the ratchet's comparison come through here, as they came through
// bundleMark before: a mark installed under one anchor set and compared under
// another is the seam this function exists not to have.
func bundleMarkAnyAnchor(raw []byte, anchors [][]byte) markRecord {
	//: In order, and the first anchor that authenticates these bytes decides.
	//: An anchor that does not is silent here rather than a failure: a mark is
	//: evidence or it is nothing, and there is no third answer to report.
	for _, anchor := range anchors {
		//: Whatever this anchor entitles the document to prove.
		if mark := bundleMark(raw, anchor); mark.present {
			//: Evidence, under an anchor still accepted.
			return mark
		}
	}
	//: No accepted anchor vouches for these bytes; they prove nothing.
	return markRecord{}
}
