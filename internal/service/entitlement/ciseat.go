// Package entitlement - authorising a CI run instead of a device.
//
// A proven CI run costs no device seat, so it is tried before the device path.
// Every failure here is silent and falls through: not being in CI, having no
// token, not being covered are all ordinary situations, and the device check
// is what answers next. Only a device failure is ever reported to a human,
// because only a device failure is something they can act on.
package entitlement

import (
	"crypto/rsa"
	"fmt"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// ciSeat authorises a proven CI run, or explains why it cannot.
//
// Every failure is silent to the caller by design: not being in CI, not having
// a token, not being covered are all ordinary, and the device path is what
// answers next. Only a device failure is ever reported to a human, because
// only a device failure is something they can act on.
func (s *Service) ciSeat(roster *coreent.RosterValue, now time.Time) (grant coreent.GrantValue, err error) {
	//: Nothing to try outside Actions, and no reason to fetch a key set.
	if !InCI() {
		//: Report it as unprovable; the caller falls through.
		return coreent.GrantValue{}, fmt.Errorf("%w: not running in GitHub Actions", coreent.ErrCIUnverifiable)
	}
	//: A roster with no CI block entitles nobody, so the round trips below
	//: would be spent to reach a refusal that is already known.
	if len(roster.CIAccounts) == 0 {
		//: Report the refusal without touching the network.
		return coreent.GrantValue{}, fmt.Errorf("%w: roster grants no CI entitlement", coreent.ErrCINotEntitled)
	}

	keys, keysErr := s.publishedJWKS()
	//: A key set we cannot obtain leaves the token unverifiable. Refusing is
	//: the only safe direction: granting a seat because the check could not
	//: run would make blocking one endpoint an entitlement.
	if keysErr != nil {
		//: Propagate the unverifiable case.
		return coreent.GrantValue{}, keysErr
	}

	claims, verifyErr := VerifyCI(s.bearerFetch, keys, roster, s.product.audience(), now)
	//: Propagate whatever refused: unverifiable token, or an account the
	//: roster does not cover.
	if verifyErr != nil {
		//: Report the refusal.
		return coreent.GrantValue{}, verifyErr
	}

	entitlement, entitlementErr := roster.CIEntitlementFor(claims.RepositoryOwnerID, now)
	//: VerifyCI already established this, so a failure here would mean the
	//: roster changed underfoot between two reads of the same value. Refuse
	//: rather than grant a seat whose deadline could not be computed.
	if entitlementErr != nil {
		//: Propagate the refusal.
		return coreent.GrantValue{}, entitlementErr
	}
	//: The subject names the run rather than a device, so `license status`
	//: and any log say which repository was authorised.
	return coreent.GrantValue{
		Subject:    "ci:" + claims.Repository,
		VerifiedAt: now,
		NotAfter:   coreent.GrantDeadline(now, roster.ExpiresAt, entitlement.ExpiresAt),
	}, nil
}

// publishedJWKS fetches GitHub's signing keys.
//
// Bounded by the same reader the roster fetch uses: the endpoint is untrusted
// by construction, and an unbounded read hands it a memory-exhaustion lever.
//
// It reuses that reader and therefore inherits its VOCABULARY, which is the
// subtle part: s.fetch speaks in coreent.ErrRosterUnreachable because its other caller
// is fetching a roster. A JWKS outage is not a roster outage, and letting that
// sentinel out of here mislabels it — the endpoint that went down is GitHub's
// key set, not the licence roster. Worse, it is not only a wording problem:
// licenseExitCode and licenseAdvice both match coreent.ErrRosterUnreachable ahead of
// the CI sentinels, so a strict CI refusal during a GitHub outage exited
// "cannot reach the licence roster" and sent its operator to check a network
// path that was working. The cause is kept as TEXT and the chain stops here.
func (s *Service) publishedJWKS() (keys map[string]*rsa.PublicKey, err error) {
	raw, fetchErr := s.fetch(ActionsJWKSURL)
	//: Unreachable keys mean the token cannot be checked at all.
	if fetchErr != nil {
		//: Report it as unverifiable rather than as a refusal, and as a CI
		//: failure ONLY: %s and not %w, so the roster vocabulary this borrowed
		//: does not travel out with it.
		return nil, fmt.Errorf("%w: fetching the key set: %s", coreent.ErrCIUnverifiable, fetchErr.Error())
	}
	//: Parse refuses a set with no usable key rather than returning an empty
	//: map, which would report every token as an unknown kid.
	return ParseJWKS(raw)
}

// ciRefusalIsFinal reports whether a ciSeat failure must be REPORTED rather
// than fallen through.
//
// Inside GitHub Actions it is final by DEFAULT, which is the inversion this
// function carries. Falling through used to be unconditional: a runner that
// also held a device key could use it, so a single copied key bought unlimited
// CI without any CI entitlement. For a scheme whose whole point is to meter CI,
// that is the hole, and it was open by construction rather than by oversight.
//
// Two conditions still gate it, both load-bearing. InCI, because a laptop never
// had a seat to prove and refusing one would be a far worse defect than the
// hole being closed. And the roster's CIRelaxed, because a self-hosted runner
// legitimately carrying a device key is a real deployment the vendor may want
// to keep working — stated once, in the one document nobody else can write.
//
// The direction matters. This field can only RELAX, so absence — which is what
// any tampering, truncation or substitution produces — reads as strict. There
// is no way to forge a downgrade; the signature is what makes that true.
//
// A vendor that publishes no `ci` block at all now refuses every CI run, and
// that is deliberately NOT special-cased: it is a coherent statement rather
// than a malformed value, it fails loudly with an error naming the account, and
// one re-signature undoes it. It is also the deployment step that has to happen
// before the next release ships.
func ciRefusalIsFinal(roster *coreent.RosterValue) bool {
	//: A nil roster cannot have relaxed anything, and this path must never do
	//: worse than fall back to the device check — taking the process down over
	//: a CI seat is very much worse.
	if roster == nil {
		//: Fall through, as every caller before this existed did.
		return false
	}
	//: Strict wherever a seat was provable, unless the vendor said otherwise.
	return InCI() && !roster.CIRelaxed
}

// ciContext annotates a device refusal with the CI failure that preceded it.
//
// Every ciSeat failure is swallowed so the device path can answer, which is
// right up to the moment the device path ALSO refuses: the reason the seat did
// not work out — the roster covers no CI account, the token was minted for
// another owner, the key set was unreachable — is the actionable half on a
// runner, and it was the half being dropped entirely.
//
// The CI cause is folded in as TEXT — ciErr.Error(), behind %s — and never as
// a wrapped error, and that is the whole safety of this function. Wrapping it would splice the seat's error CHAIN into the
// device refusal, and that chain is not limited to CI sentinels: publishedJWKS
// goes through the same bounded fetch the roster does, so a JWKS outage carries
// coreent.ErrRosterUnreachable inside coreent.ErrCIUnverifiable. licenseExitCode matches
// coreent.ErrRosterUnreachable BEFORE coreent.ErrLicenseExpired, and licenseAdvice does the
// same — so an expired licence on a runner during a GitHub outage would have
// been reported as "cannot reach the licence roster" and exited with the wrong
// code. Taking the text and dropping the chain is exactly the split wanted:
// the device sentinel stays the only one any dispatch can see.
//
// It is spelled ciErr.Error() rather than %v on the error itself so that
// dropping the cause is visible AT the call site, and so KTN-ERROR-WRAP — which
// is right that an error reaching fmt.Errorf with no %w covering it is usually
// a mistake — has nothing to guess at. The operand is a string because a string
// is what is wanted.
//
// Where the text actually lands, stated plainly because it is less than it
// looks: reportLicenseRefusal prints licenseAdvice(err), which is a FIXED
// string per sentinel, so this annotation does not reach the refusal line. It
// reaches exit.Code's structured --debug log, and licenseAdvice's fallback for
// refusals that have no table entry. Making the advice line itself CI-aware is
// a change to that table, not to this function.
//
// Outside Actions there is nothing to add: the seat was never attempted, and
// "not running in GitHub Actions" is noise on a laptop.
func ciContext(deviceErr, ciErr error) error {
	//: Nothing to annotate, or nobody to annotate it for.
	if ciErr == nil || !InCI() {
		//: Report the device refusal unchanged.
		return deviceErr
	}
	//: One %w and one plain string: the device sentinel stays reachable
	//: through errors.Is, the seat's chain deliberately does not come with it.
	return fmt.Errorf("%w (the CI seat was refused too: %s)", deviceErr, ciErr.Error())
}
