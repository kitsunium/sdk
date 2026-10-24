package token

import (
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/mac/hmacsha2"
)

const (
	// headerStackLen is the decoded header a VerifyInto holds on its stack: an
	// alg, a typ and a kid fit many times over. A larger header, still inside
	// maxHeaderLen, is decoded into a buffer of its own.
	headerStackLen int = 512
	// payloadStackLen is the decoded claims object a VerifyInto holds on its
	// stack. A larger one, still inside MaxTokenLen, is decoded into a buffer
	// of its own: correct, and one allocation.
	payloadStackLen int = 4 << 10
)

// hs256Verifier is the JWS verifier bound to HMAC-SHA-256, and the one
// verifier that also implements coretoken.VerifierInto. Its own state is
// immutable like every verifier's; the mutable MAC state lives in the
// VerifyScratch each caller thread owns.
type hs256Verifier struct {
	// jwsVerifier is the binding and the policy, and Verify.
	jwsVerifier
	// secret is the key NewScratch keys each scratch's MAC with.
	secret hs256Binding
	// owner is this verifier's scratch token: a pointer no caller can reach,
	// so a scratch can only claim this verifier by being made by it.
	owner *coretoken.ScratchOwner
}

// newHS256Verifier returns the HS256 verifier around binding and policy.
func newHS256Verifier(binding hs256Binding, policy policyValue) *hs256Verifier {
	v := &hs256Verifier{secret: binding, owner: coretoken.NewScratchOwner()}
	v.bound, v.policy = binding, policy
	//: bound for life, with an owner token of its own.
	return v
}

// NewScratch returns a fresh scratch bound to this verifier, its MAC keyed
// and primed. Make one per thread, once, and reuse it.
func (v *hs256Verifier) NewScratch() *coretoken.VerifyScratch {
	//: the crypto domain's keyed HMAC, owned by the scratch from here on.
	return coretoken.NewVerifyScratch(v.owner, hmacsha2.MAC.New(v.secret.secret))
}

// VerifyInto authenticates token with the scratch s and stores its claims in
// dst, which it overwrites on entry. It refuses what Verify refuses, in the
// same order, and then a claim past dst's capacity.
//
// dst is reset before the scratch is entered, so even a busy or foreign
// scratch leaves no previous request's claims behind, and reset again on any
// refusal.
func (v *hs256Verifier) VerifyInto(s *coretoken.VerifyScratch, token []byte, dst *coretoken.ClaimsBuf) error {
	//: a nil buffer is a construction-site mistake, not a token verdict.
	if dst == nil {
		//: name the argument.
		return errs.Wrap(coretoken.PolicyMisconfigured, errs.WrapParams{},
			errs.String("knob", "ClaimsBuf"))
	}
	dst.Reset()
	//: the scratch first: busy or foreign, no MAC is computed.
	if err := s.Enter(v.owner); err != nil {
		//: ScratchBusy or ScratchForeign.
		return err
	}
	//: released on every path, a panic included.
	defer s.Leave()
	err := v.verifyInto(s, token, dst)
	//: no half-filled buffer outlives a refusal.
	if err != nil {
		dst.Reset()
	}
	//: the verdict.
	return err
}

// verifyInto is VerifyInto once the scratch is held: structure, alg, MAC,
// then the claims.
func (v *hs256Verifier) verifyInto(s *coretoken.VerifyScratch, token []byte, dst *coretoken.ClaimsBuf) error {
	seg, serr := splitCompactBytes(token, v.policy.maxTokenLen)
	//: the size and separator bounds.
	if serr != nil {
		//: propagate TooLarge or Malformed.
		return serr
	}
	//: three segments — no more, no fewer.
	if seg.count != jwsSegments {
		//: structural failure.
		return coretoken.Malformed
	}
	var headerBuf [headerStackLen]byte
	header, herr := decodeSegmentInto(headerBuf[:], seg.part[headerSegment], maxHeaderLen)
	//: a header segment that is not strict base64url.
	if herr != nil {
		//: propagate.
		return herr
	}
	view, err := readJOSEHeader(header)
	//: the one header reader.
	if err != nil {
		//: propagate.
		return err
	}
	//: the signature is decoded before the header is judged, as parseJWS does.
	return v.authenticate(s, token, &seg, &view, dst)
}

// authenticate decodes the signature, judges the header, checks the MAC and,
// only then, reads the claims.
func (v *hs256Verifier) authenticate(s *coretoken.VerifyScratch, token []byte, seg *segmentsBytes, view *headerView, dst *coretoken.ClaimsBuf) error {
	var sigBuf [maxSignatureLen]byte
	signature, gerr := decodeSegmentInto(sigBuf[:], seg.part[signatureSegment], maxSignatureLen)
	//: a signature segment that is not strict base64url.
	if gerr != nil {
		//: propagate.
		return gerr
	}
	//: the algorithm gate, before the key reaches the MAC.
	if herr := v.policy.checkHeaderView(view, coretoken.AlgorithmHS256); herr != nil {
		clear(sigBuf[:])
		//: AlgorithmNone / Malformed / AlgorithmMismatch / HeaderUnsupported.
		return herr
	}
	//: the signing input is a PREFIX of the token, taken by slicing.
	inputLen := len(seg.part[headerSegment]) + 1 + len(seg.part[payloadSegment])
	authentic := s.MACEqual(token[:inputLen], signature)
	//: the signature buffer is cleared whatever the verdict.
	clear(sigBuf[:])
	//: authenticate before reading anything the token claims about itself.
	if !authentic {
		//: one verdict for "wrong key" and "tampered bytes".
		return coretoken.SignatureInvalid
	}
	var payloadBuf [payloadStackLen]byte
	raw, derr := decodeSegmentInto(payloadBuf[:], seg.part[payloadSegment], v.policy.maxTokenLen)
	//: a payload segment that is not strict base64url.
	if derr != nil {
		//: propagate.
		return derr
	}
	err := v.policy.judgeInto(raw, dst)
	//: the decoded claims do not outlive the call on the stack either.
	clear(raw)
	//: the verdict.
	return err
}

// judgeInto holds an AUTHENTICATED claims object to the one refusal set, then
// to the policy, then stores it in dst: the caller's claims come last, so an
// overflow never hides a policy verdict.
func (p policyValue) judgeInto(raw []byte, dst *coretoken.ClaimsBuf) error {
	var idx objectIndex
	reg, rerr := readClaims(raw, p.maxClaimDepth, joseShape{}, &idx)
	//: the same refusals Verify makes.
	if rerr != nil {
		//: propagate.
		return rerr
	}
	//: the same judgement Verify makes, compared on the literals.
	if jerr := p.judgeClaims(claimFacts{
		expiry: reg.expiry, notBefore: reg.notBefore, issuedAt: reg.issuedAt,
		issuerOK:   p.issuer == "" || literalEqual(memberValue(raw, &idx, reg.at[slotIssuer]), p.issuer),
		audienceOK: p.audience == "" || audienceContains(memberValue(raw, &idx, reg.at[slotAudience]), p.audience),
	}); jerr != nil {
		//: a policy verdict.
		return jerr
	}
	//: accepted: the caller's claims, copied.
	return storeClaims(raw, &idx, &reg, dst)
}

// audienceContains reports whether an "aud" checkAudience accepted names want.
func audienceContains(raw []byte, want string) bool {
	//: absent: nothing to match.
	if raw == nil {
		//: no audience.
		return false
	}
	//: the single-recipient spelling.
	if raw[0] == '"' {
		//: compared as it is read.
		return jsonStringEqual(raw, want)
	}
	walk := newArrayStrings(raw)
	//: any member will do.
	for {
		lit, more, err := walk.next()
		//: the end, or a member checkAudience would have refused.
		if err != nil || !more {
			//: not found.
			return false
		}
		//: found.
		if jsonStringEqual(lit, want) {
			//: contained.
			return true
		}
	}
}

// storeClaims copies an accepted claims object into dst: the registered
// claims decoded, and each private claim dst declares — decoded when it is a
// string, marked wrong-typed otherwise. Nothing is truncated: a claim that
// does not fit is ClaimsBufOverflow.
func storeClaims(raw []byte, idx *objectIndex, reg *registeredValue, dst *coretoken.ClaimsBuf) error {
	var text [coretoken.ClaimsBufTextCap]byte
	//: the three registered strings.
	for _, field := range [...]struct {
		slot  int
		claim coretoken.TextClaim
	}{{slotIssuer, coretoken.TextIssuer}, {slotSubject, coretoken.TextSubject}, {slotID, coretoken.TextID}} {
		//: an absent claim stays absent.
		if at := reg.at[field.slot]; at >= 0 {
			//: decoded, or refused for its size.
			if err := storeText(dst, field.claim, text[:], idx.value(raw, at)); err != nil {
				//: ClaimsBufOverflow.
				return err
			}
		}
	}
	storeInstants(reg, dst)
	//: the audience, member by member.
	if err := storeAudience(memberValue(raw, idx, reg.at[slotAudience]), text[:], dst); err != nil {
		//: ClaimsBufOverflow.
		return err
	}
	//: the private claims the caller declared.
	return storePrivate(raw, idx, text[:], dst)
}

// storeText decodes lit through buf into dst's text claim.
func storeText(dst *coretoken.ClaimsBuf, claim coretoken.TextClaim, buf, lit []byte) error {
	n, ok := decodeTextInto(buf, lit)
	//: longer than a slot: refused, never cut.
	if !ok {
		//: the bound.
		return coretoken.ClaimsBufOverflow
	}
	//: the slot copies it.
	return dst.SetText(claim, buf[:n])
}

// storeInstants copies the three registered instants into dst.
func storeInstants(reg *registeredValue, dst *coretoken.ClaimsBuf) {
	//: exp, nbf, iat.
	for _, field := range [...]struct {
		slot  int
		claim coretoken.TimeClaim
	}{{slotExpiry, coretoken.TimeExpiry}, {slotNotBefore, coretoken.TimeNotBefore}, {slotIssuedAt, coretoken.TimeIssuedAt}} {
		//: an absent claim stays absent.
		if reg.at[field.slot] < 0 {
			continue
		}
		instant := reg.instant(field.slot)
		dst.SetUnix(field.claim, instant.Unix(), int32(instant.Nanosecond()))
	}
}

// storeAudience copies each "aud" member into dst.
func storeAudience(raw, buf []byte, dst *coretoken.ClaimsBuf) error {
	//: absent: nothing to store.
	if raw == nil {
		//: stored.
		return nil
	}
	//: the single-recipient spelling.
	if raw[0] == '"' {
		//: one member.
		return storeMember(raw, buf, dst)
	}
	walk := newArrayStrings(raw)
	//: member by member, in token order.
	for {
		lit, more, err := walk.next()
		//: the end, or a member checkAudience would have refused.
		if err != nil || !more {
			//: stored.
			return err
		}
		//: one member, or the bound.
		if serr := storeMember(lit, buf, dst); serr != nil {
			//: ClaimsBufOverflow.
			return serr
		}
	}
}

// storeMember decodes one audience member through buf and appends it.
func storeMember(lit, buf []byte, dst *coretoken.ClaimsBuf) error {
	n, ok := decodeTextInto(buf, lit)
	//: longer than a slot.
	if !ok {
		//: the bound.
		return coretoken.ClaimsBufOverflow
	}
	//: the buffer copies it, or refuses one member too many.
	return dst.AppendAudience(buf[:n])
}

// storePrivate copies each private member dst declares.
func storePrivate(raw []byte, idx *objectIndex, buf []byte, dst *coretoken.ClaimsBuf) error {
	//: a buffer declaring nothing stores nothing.
	if dst.Declared() == 0 {
		//: stored.
		return nil
	}
	//: every member, once.
	for i := range idx.count {
		slot := dst.PrivateIndex(idx.name(raw, i))
		//: undeclared, or registered — a registered name is never declared.
		if slot < 0 {
			continue
		}
		//: a private claim of another JSON type has no text to copy.
		if idx.member[i].kind != kindString {
			dst.MarkPrivate(slot, coretoken.ClaimWrongType)
			continue
		}
		n, ok := decodeTextInto(buf, idx.value(raw, i))
		//: longer than a slot.
		if !ok {
			//: the bound.
			return coretoken.ClaimsBufOverflow
		}
		//: the slot copies it.
		if err := dst.SetPrivate(slot, buf[:n]); err != nil {
			//: unreachable past decodeTextInto; kept typed.
			return err
		}
	}
	//: stored.
	return nil
}

// instant returns the decoded instant of a time slot.
func (r *registeredValue) instant(slot int) time.Time {
	//: the three time slots.
	switch slot {
	//: exp.
	case slotExpiry:
		//: decoded once by readInstants.
		return r.expiry
	//: nbf.
	case slotNotBefore:
		//: decoded once by readInstants.
		return r.notBefore
	//: iat, and nothing else.
	default:
		//: decoded once by readInstants.
		return r.issuedAt
	}
}
