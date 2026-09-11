// Package token — verification against a JWK or a JWK Set.
//
// This file holds the ONLY run-time algorithm selection in the package, and the
// selector is the KEY, never the token. A relying party fetched the key set
// from a publisher it trusts; the "kty"/"crv" members are that publisher's
// statement about their own key, and deriving the algorithm from them is
// exactly as trustworthy as the key itself. Deriving it from the token's "alg"
// header would be trusting the attacker's statement about the relying party's
// key — which is algorithm confusion, spelled out.
package token

import (
	"crypto/ecdsa"
	"crypto/x509"
	"slices"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

// NewVerifierFromJWK returns a JWT verifier bound to the algorithm key's own
// type and curve imply:
//
//	kty=oct                -> HS256
//	kty=EC,  crv=P-256     -> ES256
//	kty=OKP, crv=Ed25519   -> EdDSA
//
// P-384 and P-521 are represented by the jwk package and refused HERE with
// KeyUnsuitable. RSA never gets this far: jwk.Parse refuses it first, as
// UNSUPPORTED_KEY_TYPE, because the SDK models no RSA scheme at all. The SDK
// signs with none of them, and a verifier that pretends otherwise would promise
// an interop the crypto domain cannot honour.
//
// The key's own advisory members are enforced, not ignored: a "use" other than
// "sig", a "key_ops" that excludes "verify", or an "alg" that disagrees with
// what the key type implies, all refuse the key. A publisher who says "this key
// is for encryption" has said something, and honouring it costs nothing.
func NewVerifierFromJWK(key jwk.KeyValue, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	binding, berr := bindJWK(key)
	//: the key decides the algorithm; the token never gets a vote.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: from here the verifier is bound exactly as a hand-built one would be.
	return newJWSVerifier(binding, cfg)
}

// NewSetVerifier returns a JWT verifier that selects a key from set by the
// token's "kid" header.
//
// # The kid is a selector, not a credential
//
// The kid chooses which key to TRY. It never decides whether the token is
// valid: the signature does. So a kid naming an EC key does not let a token
// claiming HS256 through — the binding built from that key reports ES256, the
// header comparison refuses, and no key material reaches a MAC.
//
// # Duplicate kids
//
// jwk.Set.ByKid refuses to choose between keys sharing a kid, because nothing
// in the document distinguishes them and picking the first would make the
// answer depend on member order. This verifier CAN choose, because it has
// something the key set does not: a signature. It tries the candidates in
// document order and accepts the first that verifies, which is the rotation
// path RFC 7517 §4.5 leaves open.
//
// It tries a BOUNDED number of them. The candidate count is chosen by whoever
// published the JWK Set, and each attempt is a signature verification, so an
// unbounded loop would let a publisher — or anybody who can influence the
// document — price every request. Past MaxKeyCandidates the token is refused
// with KeyIDAmbiguous rather than verified slowly.
//
// # The set is bound ONCE, here
//
// Every member's verifying binding is derived at construction, not per token.
// A JWK is a key's SERIALISED form and the binding is the same key in the
// shape a primitive consumes; deriving it reads the key's own kty/crv/x/y and
// nothing a token supplies, so it is a fixed function of material that is
// fixed for this verifier's lifetime. Doing it per request cost a full PKIX
// round trip — `x509.MarshalPKIXPublicKey` immediately re-parsed by
// `x509.ParsePKIXPublicKey` — measured at 8.3 % of an ES256 JWKS
// verification's CPU and 28.2 % of its allocated objects. See
// `internal/service/crypto/jwk/BENCH.md`.
//
// What deliberately did NOT move is the ORDER. Which key is selected, the
// candidate bound, and the algorithm comparison against the binding all still
// happen per token and in the same sequence, because that sequence is ADR
// 0042's security property. Nothing here is keyed on, or cached against,
// anything the token carries.
//
// # A set that can verify nothing is refused
//
// Binding at construction is also what lets a useless set be refused there
// (ADR 0031): an empty set, and one whose every member either has no kid or is
// unusable, are both PolicyMisconfigured rather than a verifier that refuses
// every token. The unusable members of a set that IS accepted still count
// against MaxKeyCandidates — see indexByKid.
func NewSetVerifier(set jwk.Set, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	policy, perr := newPolicy(cfg)
	//: every bound and knob is validated once, here.
	if perr != nil {
		//: propagate PolicyMisconfigured.
		return nil, perr
	}
	//: an empty set can never verify anything; say so at construction.
	if set.Len() == 0 {
		//: refuse rather than build a verifier that always fails.
		return nil, errs.Wrap(coretoken.PolicyMisconfigured, errs.WrapParams{},
			errs.String("knob", "empty JWK Set"))
	}
	index := indexByKid(set)
	//: a set with members can still be that same verifier. A member without a
	//: kid is never selected — candidates refuses the empty kid first — and an
	//: unusable one is skipped by Verify, so a set holding only those refuses
	//: every token it will ever see, for exactly the reason an empty set does.
	if !selectable(index) {
		//: refuse, and say which constructor a kid-less key belongs to.
		return nil, unverifiableSet(set.Len(), indexedCount(index))
	}
	//: bound to the set, not to one key — and bound once, not once per token.
	return &setVerifier{byKid: index, policy: policy}, nil
}

// selectable reports whether index holds at least one member a token could be
// verified against: indexed under a kid, and bound to an algorithm this package
// implements.
//
// It asks only whether such a member EXISTS. An unusable member stays in the
// index, and keeps counting against MaxKeyCandidates, whatever this returns —
// that is indexByKid's rule, and this function only reads the index it built.
func selectable(index map[string][]boundKeyValue) bool {
	//: one usable member under any kid is enough to verify something.
	for _, group := range index {
		//: an unusable member can never be the one that verifies.
		if slices.ContainsFunc(group, func(member boundKeyValue) bool { return member.usable }) {
			//: this verifier can verify at least one token.
			return true
		}
	}
	//: nothing indexed, or nothing indexed that this package can verify with.
	return false
}

// indexedCount reports how many members index holds across every kid.
func indexedCount(index map[string][]boundKeyValue) int {
	count := 0
	//: every kid's group, usable or not.
	for _, group := range index {
		count += len(group)
	}
	//: the members that carried a kid.
	return count
}

// unverifiableSet is the PolicyMisconfigured refusal for a JWK Set that no
// token could ever verify against.
//
// It is built from the sentinel's own code, reason, public message and exit
// code — so errors.Is and errs.HasCode both still match PolicyMisconfigured —
// rather than by wrapping the sentinel, because origin-wins would inherit the
// sentinel's generic Private, and the one thing worth telling an operator here
// is specific: a key published without a kid is a single key, and
// NewVerifierFromJWK is the constructor that verifies with one.
func unverifiableSet(members, withKid int) error {
	//: counts only — never a kid, never key material.
	return errs.Wrap(nil, errs.WrapParams{
		Code:     coretoken.PolicyMisconfigured.Code(),
		Reason:   coretoken.PolicyMisconfigured.Reason(),
		Public:   coretoken.PolicyMisconfigured.Public(),
		Private:  "service/token: no JWK Set member has both a kid and a key this package verifies with; a key published without a kid is verified with NewVerifierFromJWK",
		ExitCode: coretoken.PolicyMisconfigured.ExitCode(),
	}, errs.String("knob", "JWK Set"), errs.Int("members", members), errs.Int("members_with_kid", withKid))
}

// setVerifier authenticates JWS compact tokens against a JWK Set.
type setVerifier struct {
	// byKid holds the set's members grouped by their "kid", in document order,
	// each with its binding already derived. Members carrying no kid are
	// absent: a lookup by id never means "match the keys that have no id",
	// which is jwk.Set.AllByKid's own rule preserved here.
	byKid map[string][]boundKeyValue
	// policy is the validated verification policy.
	policy policyValue
}

// indexByKid groups set's members by kid, deriving each member's binding once.
//
// A member the SDK verifies nothing with is INDEXED ANYWAY, as an unusable
// entry. Dropping it would silently widen MaxKeyCandidates: the bound counts
// the keys published under one kid, so a set whose kid names six keys — three
// of them P-384 — must still be refused as ambiguous rather than quietly
// resolved down to three. (Not RSA: a set carrying an RSA member never exists,
// because jwk.ParseSet refuses the whole document.) The per-token loop skips an unusable entry exactly
// as it used to skip a bind failure.
func indexByKid(set jwk.Set) map[string][]boundKeyValue {
	index := make(map[string][]boundKeyValue)
	//: document order is preserved, because the caller's ranking survives it.
	for _, key := range set.Keys() {
		kid := key.Kid()
		//: a keyless member can never be selected by kid; leave it out.
		if kid == "" {
			//: nothing to index it under.
			continue
		}
		binding, berr := bindJWK(key)
		//: the refusal is recorded, never returned — a published set
		//: legitimately holds keys for algorithms we do not do, and that was
		//: never a reason to refuse the whole verifier.
		index[kid] = append(index[kid], boundKeyValue{binding: binding, usable: berr == nil})
	}
	//: one entry per published kid.
	return index
}

// Verify selects candidate keys by kid and authenticates against them.
func (v *setVerifier) Verify(token string) (claims coretoken.ClaimsValue, err error) {
	parts, perr := v.policy.parseJWS(token)
	//: bounded parse, no key involved yet.
	if perr != nil {
		//: propagate TooLarge / Malformed / TooDeep / DuplicateMember.
		return coretoken.ClaimsValue{}, perr
	}
	candidates, cerr := v.candidates(parts.header.kid)
	//: selection failures are named separately from verification failures.
	if cerr != nil {
		//: propagate KeyIDMissing / KeyNotFound / KeyIDAmbiguous.
		return coretoken.ClaimsValue{}, cerr
	}
	//: try each candidate; the first whose signature verifies wins.
	for _, candidate := range candidates {
		//: a candidate the SDK cannot verify with is skipped, not fatal — a
		//: published set legitimately holds keys for algorithms we do not do.
		if !candidate.usable {
			continue
		}
		//: the algorithm gate, per candidate, before any primitive runs.
		if herr := v.policy.checkHeader(parts.header, candidate.binding.algorithm()); herr != nil {
			continue
		}
		//: authenticate.
		if candidate.binding.verify(parts.input, parts.signature) {
			//: authenticated: now the claims may be decoded and judged.
			return v.policy.decodeAndValidate(parts.payload, joseShape{})
		}
	}
	//: no candidate authenticated the token. The verdict is deliberately the
	//: same as a single-key failure: which of several keys did not match is
	//: not information a bearer is owed.
	return coretoken.ClaimsValue{}, coretoken.SignatureInvalid
}

// candidates resolves the keys to try for kid, refusing both ends.
func (v *setVerifier) candidates(kid string) (keys []boundKeyValue, err error) {
	//: a set verifier selects by id; it does not try everything it holds.
	if kid == "" {
		//: refuse, and name the missing header. This check stays FIRST: the
		//: index deliberately holds no entry for the empty kid, but a lookup
		//: that reached it must never be the thing that decides.
		return nil, KeyIDMissing
	}
	matches := v.byKid[kid]
	//: no key under that id.
	if len(matches) == 0 {
		//: refuse without saying which ids the set does hold.
		return nil, KeyNotFound
	}
	//: more candidates than the bound: refuse rather than verify slowly.
	if len(matches) > v.policy.maxKeyCandidates {
		//: name the limit, never the kid.
		return nil, errs.Wrap(KeyIDAmbiguous, errs.WrapParams{},
			errs.Int("limit", v.policy.maxKeyCandidates))
	}
	//: one, or a bounded rotation window.
	return matches, nil
}

// bindJWK maps a JWK onto the verifying binding its own type and curve imply.
func bindJWK(key jwk.KeyValue) (binding verifyingKey, err error) {
	//: the publisher's advisory members are honoured before the material is.
	if uerr := checkJWKUsage(key); uerr != nil {
		//: propagate KeyUnsuitable.
		return nil, uerr
	}
	//: the mapping. It reads kty and crv — the KEY's members — and nothing
	//: from any token.
	switch {
	//: a symmetric JWK is an HS256 secret; core/crypto.Key enforces 256 bits.
	case key.Kty() == jwk.TypeOct:
		//: bound to HMAC-SHA-256.
		return bindOctJWK(key)
	//: the only EC curve the SDK signs on.
	case key.Kty() == jwk.TypeEC && key.Crv() == jwk.CurveP256:
		//: bound to ES256.
		return bindECJWK(key)
	//: the Edwards curve behind JOSE EdDSA.
	case key.Kty() == jwk.TypeOKP && key.Crv() == jwk.CurveEd25519:
		//: bound to EdDSA.
		return bindOKPJWK(key)
	//: P-384, P-521 and any other curve jwk represents: representable, not
	//: verifiable. RSA never reaches this switch — jwk.Parse refuses it.
	default:
		//: refuse rather than promise an interop the SDK cannot honour.
		return nil, keyUnsuitable("no token algorithm for this kty/crv")
	}
}

// bindOctJWK binds a symmetric JWK to HS256.
func bindOctJWK(key jwk.KeyValue) (binding verifyingKey, err error) {
	secret, serr := key.Secret()
	//: jwk.Secret enforces the 256-bit core/crypto.Key length.
	if serr != nil {
		//: a symmetric JWK that is not a usable secret.
		return nil, keyUnsuitable("oct JWK is not a 256-bit symmetric key")
	}
	//: bound to HS256 and nothing else.
	return bindSecret(secret)
}

// bindECJWK binds a P-256 JWK to ES256.
func bindECJWK(key jwk.KeyValue) (binding verifyingKey, err error) {
	der, derr := key.ECDSAPublic()
	//: jwk validates the point on the curve before it renders it.
	if derr != nil {
		//: propagate as a key verdict.
		return nil, keyUnsuitable("EC JWK does not render a PKIX public key")
	}
	parsed, perr := x509.ParsePKIXPublicKey(der)
	//: the DER came from jwk one line ago, so a failure here is a bug, not input.
	if perr != nil {
		//: still typed, never a panic.
		return nil, keyUnsuitable("PKIX round-trip of an EC JWK failed")
	}
	public, isECDSA := parsed.(*ecdsa.PublicKey)
	//: the type assertion cannot fail for a kty=EC key, but an assertion that
	//: cannot fail is still an assertion someone will make fail later.
	if !isECDSA {
		//: refuse.
		return nil, keyUnsuitable("EC JWK did not decode to an ECDSA public key")
	}
	//: bound to ES256, with the curve and point re-checked.
	return bindP256Public(public)
}

// bindOKPJWK binds an Ed25519 JWK to EdDSA.
func bindOKPJWK(key jwk.KeyValue) (binding verifyingKey, err error) {
	public, perr := key.Ed25519Public()
	//: jwk validates the length and the seed/public consistency.
	if perr != nil {
		//: propagate as a key verdict.
		return nil, keyUnsuitable("OKP JWK does not render an Ed25519 public key")
	}
	//: bound to JOSE EdDSA — not to PASETO v4.public, which uses the same
	//: primitive over different bytes and carries its own Algorithm.
	return bindEd25519Public(public, coretoken.AlgorithmEdDSA)
}

// checkJWKUsage honours the key's own advisory members: "use", "key_ops" and
// "alg". They are the publisher's statement about their key, and a relying
// party that ignores them is choosing to know less than it was told.
func checkJWKUsage(key jwk.KeyValue) error {
	//: "use" is optional; when present it must permit signatures.
	if use := key.Use(); use != "" && use != "sig" {
		//: a key published for encryption is not a key for verifying tokens.
		return keyUnsuitable("JWK use is not sig")
	}
	//: "key_ops" is optional; when present it must include "verify".
	if ops := key.KeyOps(); len(ops) != 0 && !slices.Contains(ops, "verify") {
		//: the publisher excluded the operation we were about to perform.
		return keyUnsuitable("JWK key_ops does not include verify")
	}
	implied, known := impliedAlg(key)
	//: "alg" is optional; when present it must be the algorithm the key type
	//: implies, or the publisher and the key disagree about the key.
	if alg := key.Alg(); alg != "" && (!known || alg != implied) {
		//: refuse the contradiction rather than pick a side.
		return keyUnsuitable("JWK alg contradicts its kty/crv")
	}
	//: the publisher's statements are consistent with what we intend to do.
	return nil
}

// impliedAlg reports the JOSE algorithm name a key's type and curve imply.
// The second result is false when this package implements none for it, which
// is a different statement from "the empty algorithm" and keeps the caller
// from comparing a publisher's "alg" against nothing at all.
func impliedAlg(key jwk.KeyValue) (alg string, known bool) {
	//: the same mapping bindJWK uses, rendered as a JOSE name.
	switch {
	//: symmetric keys carry HMAC-SHA-256 here.
	case key.Kty() == jwk.TypeOct:
		//: JOSE HS256.
		return coretoken.AlgorithmHS256.String(), true
	//: the only EC curve the SDK signs on.
	case key.Kty() == jwk.TypeEC && key.Crv() == jwk.CurveP256:
		//: JOSE ES256.
		return coretoken.AlgorithmES256.String(), true
	//: the Edwards curve behind JOSE EdDSA.
	case key.Kty() == jwk.TypeOKP && key.Crv() == jwk.CurveEd25519:
		//: JOSE EdDSA.
		return coretoken.AlgorithmEdDSA.String(), true
	//: no algorithm implied; bindJWK refuses this key anyway.
	default:
		//: nothing to compare a publisher's alg against.
		return "", false
	}
}
