// Package entitlement - proving a run is CI, rather than taking its word for it.
//
// A CI seat is free, so "am I in CI?" becomes a question worth lying about.
// Every environment variable that answers it — CI, GITHUB_ACTIONS, the absence
// of a TTY, the hostname — is one `export` away from being whatever the caller
// wants, so granting anything on that basis makes the device quota decorative.
//
// GitHub Actions can answer it properly. A workflow granted `id-token: write`
// can exchange two runner-injected values for a JWT signed by GitHub, whose
// claims name the repository and its owner. The request token is an ephemeral
// runner secret: someone outside Actions cannot obtain one, and nobody can
// forge the signature without GitHub's private key.
//
// What this does NOT prove is that the process holding the token is the job it
// was minted for. A token exfiltrated from a legitimate run stays usable off-CI
// until it expires. Closing that needs a server-side nonce, which the offline
// verification model rules out, so the residue is accepted and stated.
//
// Stated as what it IS, which is not what this comment used to claim. "The
// exposure is one free seat" was never demonstrated and is not true. A short
// token bounds the DURATION a stolen proof keeps working; it says nothing about
// the NUMBER of processes that can present it at once. Nothing ties a token to a
// consumer: VerifyActionsToken is pure and offline, it keeps no record of what it
// has already admitted, and two verifiers could not share one if it did. So one
// leaked token satisfies every verifier it reaches, all of them at the same time,
// for as long as it is valid.
//
// The honest bound is ONE FREE SEAT PER VERIFIER FOR UP TO 32 MINUTES:
// maxTokenLifetime (30 min) is the widest window a token may claim, clockSkew
// (2 min) is what checkTiming allows on top when admitting one, and their sum is
// how long after minting a stolen token still verifies. What makes that a bound
// rather than a sentence is ciseat.go handing the token's own expiry to
// coreent.GrantDeadline — without it the GRANT outlived the proof by up to a day,
// and the duration this paragraph names described nothing at all.
package entitlement

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// ActionsIssuer is the only issuer whose tokens mean anything here.
const ActionsIssuer string = "https://token.actions.githubusercontent.com"

// DefaultActionsAudience is the audience a product that names none falls back
// to. A product SHOULD set its own: an audience shared between products lets a
// token minted for one satisfy the other's gate.
//
// A dedicated audience matters: a token minted for some other service — a
// cloud provider, a registry — must not be replayable here, and `aud` is what
// keeps the two apart. It is chosen by us, not by the caller, precisely so a
// workflow cannot request a token this verifier will accept for a purpose it
// never agreed to.
const DefaultActionsAudience string = "kitsunium-sdk-entitlement"

// maxTokenBytes caps a compact JWT. Real Actions tokens are two kilobytes at
// most; anything past this is not a token we should be parsing.
const maxTokenBytes int = 16 << 10

// maxTokenLifetime bounds how long a token may claim to be valid.
//
// GitHub mints these for minutes. A token whose own window is wider is either
// not from Actions or is being replayed, and accepting it would turn the skew
// allowance below into an unbounded replay window.
const maxTokenLifetime time.Duration = 30 * time.Minute

// clockSkew is how far the local clock may disagree with GitHub's before a
// valid token is refused. CI runners are well synchronised; this is slack for
// the round trip, not for a drifting clock.
const clockSkew time.Duration = 2 * time.Minute

// minRSAModulusBits refuses a key too small to mean anything. GitHub signs
// with 2048-bit keys; a JWKS offering less has been tampered with.
const minRSAModulusBits int = 2048

// jwtParts is the number of segments in a compact JWS: header, payload,
// signature.
const jwtParts int = 3

// Positions of the three segments in a compact JWS.
const (
	// headerSegment is the JOSE header.
	headerSegment int = iota
	// payloadSegment is the claims set.
	payloadSegment
	// signatureSegment is the signature over the first two.
	signatureSegment
)

// minRSAExponent is the smallest public exponent RSA allows. Anything below
// is not a key.
const minRSAExponent int64 = 3

// maxRSAExponent bounds the public exponent so the int64 it is read into
// cannot truncate when narrowed to int, which is 32 bits on some platforms.
// Real exponents are 65537; this is far above any of them.
const maxRSAExponent int64 = 1<<31 - 1

// evenDivisor tests an exponent's parity. An even RSA exponent shares a factor
// with every Euler totient, so it cannot be a valid public exponent.
const evenDivisor int64 = 2

// ActionsClaimsValue is what a verified GitHub Actions token asserts.
//
// Only the claims this package acts on are decoded. Everything else GitHub
// puts in the token is deliberately ignored: a field that is not read cannot
// be relied on by accident.
type ActionsClaimsValue struct {
	// Issuer must be ActionsIssuer; anything else is another identity
	// provider entirely.
	Issuer string `json:"iss"`
	// Subject identifies the workflow context. It is recorded for diagnostics
	// and policy, never parsed for ownership: its format varies with
	// environments, customisation and GitHub's immutable-subject rollout, so
	// reading an owner out of it would break silently.
	Subject string `json:"sub"`
	// Audience is who the token was minted for. GitHub emits a single string;
	// the JWT spec allows an array, so both are accepted on decode.
	Audience audienceClaim `json:"aud"`
	// RepositoryOwnerID is the entitlement key: the account id, which survives
	// renames and cannot be reused. RepositoryOwner is the display name for
	// the same account and must never be what a licence matches on — a freed
	// handle can be claimed by someone else.
	//
	// GitHub sends a decimal string and nothing here VALIDATES that: the only
	// check is that it is non-empty, and the value is compared to the roster's
	// keys by string equality. "Numeric" therefore describes what the issuer
	// emits, not a property this package enforces — and it does not need to,
	// because a value that is not one of the roster's keys is not entitled
	// whatever its shape. A decimal check would be a second syntax for a
	// comparison that is already exact.
	RepositoryOwnerID string `json:"repository_owner_id"`
	// RepositoryOwner is the account's current login, for diagnostics only.
	RepositoryOwner string `json:"repository_owner"`
	// Repository is "owner/name", for diagnostics and optional narrowing.
	Repository string `json:"repository"`
	// EventName is the workflow trigger, DECODED AND READ BY NOTHING. It is
	// carried so a diagnostic can print it and so a future policy would not
	// have to change the wire shape to see it.
	//
	// It used to say pull_request_target "runs with the base repository's
	// identity while potentially executing code from a fork, so a policy may
	// want to refuse it" — which is true of Actions and describes no
	// behaviour of this package. Four lines above, this type's own doc
	// comment says a field that is not read cannot be relied on by accident;
	// a field whose comment describes a policy is exactly how that gets
	// relied on. Test_VerifyActionsToken_ignoresTheClaimsNothingReads makes
	// the silence mechanical.
	EventName string `json:"event_name"`
	// RunnerEnvironment distinguishes "github-hosted" from "self-hosted",
	// DECODED AND READ BY NOTHING, for the same reason and with the same
	// warning as EventName above.
	RunnerEnvironment string `json:"runner_environment"`
	// ExpiresAt, IssuedAt and NotBefore are NumericDate seconds.
	ExpiresAt int64 `json:"exp"`
	// IssuedAt is when GitHub minted the token.
	IssuedAt int64 `json:"iat"`
	// NotBefore is when the token becomes usable.
	NotBefore int64 `json:"nbf"`
}

// audienceClaim decodes an `aud` that may be a string or an array of strings.
type audienceClaim []string

// UnmarshalJSON accepts both spellings the JWT spec allows.
func (a *audienceClaim) UnmarshalJSON(data []byte) error {
	//: A null audience is not "the empty string": decoding it as one would
	//: produce a claim of [""], which an empty expected audience would then
	//: match. Refuse it outright rather than invent a value.
	if string(data) == "null" {
		//: Refuse an absent audience.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_audience"),
			errs.String("condition", "the claim is null rather than absent or a string"))
	}
	var single string
	//: The single-string form is what GitHub emits today.
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audienceClaim{single}
		//: Decoded the common shape.
		return nil
	}
	var many []string
	//: The array form is legal and must not be a parse failure.
	if err := json.Unmarshal(data, &many); err != nil {
		//: Neither shape: refuse rather than treat the audience as absent,
		//: which would skip the check that keeps another service's token from
		//: being replayed here.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_audience"),
			errs.String("condition", "neither of the two shapes the JWT spec allows"))
	}
	*a = many
	//: Decoded the array shape.
	return nil
}

// contains reports whether the audience includes want.
//
// Exact match only: no prefix rule and no case folding, either of which would
// widen what this verifier accepts beyond what it was asked for.
func (a *audienceClaim) contains(want string) bool {
	//: A token minted for another service must not be usable here.
	return slices.Contains(*a, want)
}

// jwtSegments splits a compact JWS and returns its parts plus the exact bytes
// the signature covers.
//
// The signing input is the ORIGINAL ASCII of "header.payload", not a
// re-serialisation of the decoded values: re-encoding would not reproduce the
// same bytes, and the signature covers those bytes and nothing else.
func jwtSegments(raw string) (signingInput string, header, payload, signature []byte, err error) {
	//: An oversized token is not one we should spend memory parsing.
	if len(raw) > maxTokenBytes {
		//: Refuse before decoding anything.
		return "", nil, nil, nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "split_token"),
			errs.String("condition", "token at or past the cap"),
			errs.Int("limit_bytes", maxTokenBytes),
			errs.Int("got_bytes", len(raw)))
	}
	parts := strings.Split(raw, ".")
	//: Exactly three non-empty segments; a JWS with fewer is not one, and an
	//: empty signature segment is the "alg: none" shape by another route.
	if len(parts) != jwtParts || parts[headerSegment] == "" ||
		parts[payloadSegment] == "" || parts[signatureSegment] == "" {
		//: Refuse a malformed token.
		return "", nil, nil, nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "split_token"),
			errs.String("condition", "not three non-empty segments"))
	}

	decoded := make([][]byte, jwtParts)
	//: Strict, unpadded base64url: RawURLEncoding rejects padding and the
	//: alternative alphabet, both of which would let two different strings
	//: decode to the same bytes.
	for i, part := range parts {
		segment, decodeErr := base64.RawURLEncoding.DecodeString(part)
		//: A segment we cannot decode cannot be authenticated.
		if decodeErr != nil {
			//: Refuse rather than guess at the encoding.
			return "", nil, nil, nil, classify(coreent.ErrCIUnverifiable, decodeErr,
				errs.String("stage", "split_token"),
				errs.Int("segment", i))
		}
		decoded[i] = segment
	}
	//: Return the parts and the exact signed bytes.
	return parts[headerSegment] + "." + parts[payloadSegment],
		decoded[headerSegment], decoded[payloadSegment], decoded[signatureSegment], nil
}

// jwtHeader is the subset of the JOSE header this package acts on.
type jwtHeader struct {
	// Algorithm must be RS256; nothing else is accepted.
	Algorithm string `json:"alg"`
	// KeyID selects which JWKS key signed this token.
	KeyID string `json:"kid"`
	// Type is optional, and must be JWT when present.
	Type string `json:"typ"`
	// Critical lists extensions a verifier must understand. We understand
	// none, so any value here is a refusal.
	Critical []string `json:"crit"`
	// JWKSetURL and X509URL are places a hostile token can point a naive
	// verifier at. They are decoded ONLY so a NON-EMPTY value can be refused —
	// honouring either would let the token nominate its own trust anchor.
	//
	// Non-empty, and the precision matters: absent, `""` and `null` all decode
	// to the empty string and are therefore indistinguishable here, so none of
	// the three is refused. That is harmless rather than overlooked — neither
	// field CHOOSES a key source in this implementation, so an empty one
	// nominates nothing — but "their presence is refused" is what this comment
	// used to say, and it was not true of `"jku": ""`.
	JWKSetURL string `json:"jku"`
	// X509URL is refused for the same reason as JWKSetURL, on the same
	// non-empty condition.
	X509URL string `json:"x5u"`
}

// strictUnmarshal decodes exactly one JSON object, refusing trailing input.
//
// encoding/json stops at the first value, so "{}{...}" would otherwise decode
// as the first object and silently ignore whatever followed — a way to show a
// verifier one document and a reader another.
func strictUnmarshal[T any](data []byte, what string) (decoded T, err error) {
	//: A JWT header and a claims set are OBJECTS. `null` decodes into the
	//: zero value without error, which would reach the field checks as an
	//: empty header rather than as the malformed input it is — and every
	//: other JSON scalar has the same problem.
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		//: Refuse anything that is not a JSON object.
		return decoded, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_"+what),
			errs.String("condition", "not a JSON object"))
	}

	//: A third check, and neither of the two above was it: refusing a non-object
	//: and refusing a trailing document say nothing about a member named twice.
	//: json.Decoder keeps the LAST, so {"alg":"none","alg":"RS256"} verifies as
	//: RS256 here while a reader keeping the first sees "none" — the algorithm
	//: confusion checkHeaderShape exists to refuse, reintroduced by the parser.
	//: One branch covers the token header, the claim set and the mint response.
	//: RFC 8725 §2.6.
	if member, duplicate := checkNoDuplicateNames(data); duplicate {
		//: Unverifiable: two readers of this token disagree on its claims.
		return decoded, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_"+what),
			errs.String("condition", "one member name declared twice, which two readers can read differently"),
			errs.String("member", member))
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	//: Unknown fields are tolerated: GitHub adds claims over time and a new
	//: one must not break verification. What is refused is a second document.
	if decodeErr := decoder.Decode(&decoded); decodeErr != nil {
		//: Refuse what we cannot decode.
		return decoded, classify(coreent.ErrCIUnverifiable, decodeErr,
			errs.String("stage", "decode_"+what))
	}
	//: More() only reports whether another VALUE follows, so `{...}]junk`
	//: slips past it. Requiring the next read to be EOF is what actually
	//: says "this input was one document and nothing else".
	if _, eofErr := decoder.Token(); !errors.Is(eofErr, io.EOF) {
		//: Refuse rather than act on the first of several.
		return decoded, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_"+what),
			errs.String("condition", "a second document follows the first"))
	}
	//: One well-formed document.
	return decoded, nil
}

// checkHeaderShape refuses the header fields that decide whether verifying the
// signature can mean anything: the algorithm, the type, and any extension the
// token declares critical.
func checkHeaderShape(header *jwtHeader) error {
	//: RS256 and nothing else. Accepting "none" would make every token valid,
	//: and accepting an HMAC alg would let a caller sign with the PUBLIC key —
	//: the classic algorithm-confusion attack, since that key is published.
	if header.Algorithm != "RS256" {
		//: Refuse any other algorithm outright.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_header"),
			errs.String("condition", "an algorithm this verifier will not accept"),
			errs.String("alg", header.Algorithm))
	}
	//: A typ, when present, must say JWT.
	if header.Type != "" && !strings.EqualFold(header.Type, "JWT") {
		//: Refuse a token typed as something else.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_header"),
			errs.String("condition", "typed as something other than a JWT"),
			errs.String("typ", header.Type))
	}
	//: "crit" names extensions the verifier MUST understand. We implement
	//: none, so the only correct answer to any of them is to refuse.
	if len(header.Critical) != 0 {
		//: Refuse rather than ignore something declared critical.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_header"),
			errs.String("condition", "extensions declared critical that this verifier does not implement"),
			errs.String("crit", strings.Join(header.Critical, ",")))
	}
	//: A well-formed header we know how to act on.
	return nil
}

// checkHeader refuses every header shape that would make verification unsound,
// and returns the key the token names.
func checkHeader(raw []byte) (kid string, err error) {
	header, decodeErr := strictUnmarshal[jwtHeader](raw, "token header")
	//: A header we cannot decode cannot select a key.
	if decodeErr != nil {
		//: Propagate the decode failure.
		return "", decodeErr
	}
	//: The algorithm and extension checks are what make the signature step
	//: mean anything at all.
	if shapeErr := checkHeaderShape(&header); shapeErr != nil {
		//: Propagate the shape failure.
		return "", shapeErr
	}
	//: A token must not choose where its own key comes from. Honouring jku or
	//: x5u would let it nominate a JWKS the attacker controls, which is the
	//: whole trust decision handed to the thing being verified.
	if header.JWKSetURL != "" || header.X509URL != "" {
		//: Refuse a self-nominated trust anchor.
		return "", refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_header"),
			errs.String("condition", "the token nominates its own key source"))
	}
	//: Without a kid there is nothing to select, and trying every key would
	//: turn a rotation into an accepted forgery surface.
	if header.KeyID == "" {
		//: Refuse an unaddressed token.
		return "", refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_header"),
			errs.String("condition", "no kid, so no key to select"))
	}
	//: Return the key this token names.
	return header.KeyID, nil
}

// verifySignature checks the RS256 signature over the signing input.
func verifySignature(signingInput string, signature []byte, key *rsa.PublicKey) error {
	//: A signature of the wrong length is not a PKCS#1 v1.5 signature for
	//: this key, and rsa.VerifyPKCS1v15 would reject it anyway — refusing
	//: here names the reason instead.
	if len(signature) != key.Size() {
		//: Refuse a signature that cannot belong to this key.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "verify_signature"),
			errs.String("condition", "signature length does not match the key"),
			errs.Int("got_bytes", len(signature)),
			errs.Int("want_bytes", key.Size()))
	}
	digest := sha256.Sum256([]byte(signingInput))
	//: This is the step everything else exists to make meaningful.
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		//: Forged, tampered with, or signed by another key.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "verify_signature"),
			errs.String("condition", "forged, tampered with, or signed by another key"))
	}
	//: Authenticated.
	return nil
}

// checkTokenWindow refuses a window that is not one: inverted, or wider than
// GitHub ever issues.
//
// The width bound is what keeps the clock-skew allowance from becoming an
// unbounded replay window, and the inversion check has to come first — a
// negative duration sails straight past a "greater than" comparison.
func checkTokenWindow(issued, expires time.Time) error {
	//: A token that expires before it was issued is not one.
	if expires.Before(issued) {
		//: Refuse an inverted window.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_window"),
			errs.String("condition", "expires before it was issued"))
	}
	//: A window wider than GitHub ever issues is either not from Actions or
	//: is being replayed, whoever signed it.
	if expires.Sub(issued) > maxTokenLifetime {
		//: Refuse an over-wide window.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_window"),
			errs.String("condition", "a window wider than Actions ever issues"),
			errs.String("limit", maxTokenLifetime.String()),
			errs.String("window", expires.Sub(issued).String()))
	}
	//: A window of a plausible width.
	return nil
}

// checkTiming refuses a token outside its own validity window.
func checkTiming(claims *ActionsClaimsValue, now time.Time) error {
	//: A token with no expiry would be valid forever; GitHub always sets one,
	//: so its absence means this is not the document we think it is.
	if claims.ExpiresAt == 0 || claims.IssuedAt == 0 {
		//: Refuse a token that does not bound itself.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_timing"),
			errs.String("condition", "the token does not bound itself"))
	}
	expires := time.Unix(claims.ExpiresAt, 0)
	issued := time.Unix(claims.IssuedAt, 0)
	//: Past its expiry, with a small allowance for clock disagreement.
	if now.After(expires.Add(clockSkew)) {
		//: Refuse an expired token.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_timing"),
			errs.String("condition", "past its expiry, allowance included"),
			errs.String("expires_at", expires.UTC().Format(time.RFC3339)))
	}
	//: Issued in the future beyond the skew: either a forgery or a clock too
	//: wrong to reason about.
	if issued.After(now.Add(clockSkew)) {
		//: Refuse a token from the future.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_timing"),
			errs.String("condition", "issued in the future, allowance included"),
			errs.String("issued_at", issued.UTC().Format(time.RFC3339)))
	}
	//: nbf is optional, but binding when present.
	if claims.NotBefore != 0 && time.Unix(claims.NotBefore, 0).After(now.Add(clockSkew)) {
		//: Refuse a token that is not usable yet.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_timing"),
			errs.String("condition", "not-before is still ahead, allowance included"),
			errs.String("not_before", time.Unix(claims.NotBefore, 0).UTC().Format(time.RFC3339)))
	}
	//: The window's own shape is checked last, and separately: it is a
	//: property of the token rather than of the moment it is read at.
	return checkTokenWindow(issued, expires)
}

// checkClaims refuses a token that is authentic but not addressed to us.
func checkClaims(claims *ActionsClaimsValue, audience string, now time.Time) error {
	//: Authenticity says GitHub minted it; the issuer check says GitHub is
	//: who we asked. Both are needed — a valid token from another provider
	//: would otherwise pass on signature alone if the JWKS ever widened.
	if claims.Issuer != ActionsIssuer {
		//: Refuse another issuer.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_claims"),
			errs.String("condition", "another identity provider entirely"),
			errs.String("issuer", claims.Issuer),
			errs.String("want_issuer", ActionsIssuer))
	}
	//: An empty expected audience would match a token carrying an empty one,
	//: turning the check off exactly where it matters. A caller with nothing
	//: to ask for is a programming error, not a permissive default.
	if audience == "" {
		//: Refuse rather than verify against nothing.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_claims"),
			errs.String("condition", "the caller named no audience, which would match a token carrying none"))
	}
	//: A token minted for a cloud provider or a registry must not be
	//: replayable here.
	if !claims.Audience.contains(audience) {
		//: Refuse a token minted for something else.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_claims"),
			errs.String("condition", "minted for another service"),
			errs.String("want_audience", audience))
	}
	//: The subject is not parsed for ownership, but its absence means the
	//: token is not the shape Actions produces.
	if claims.Subject == "" {
		//: Refuse a subjectless token.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_claims"),
			errs.String("condition", "no subject, which is not the shape Actions produces"))
	}
	//: The owner id is the entitlement key. Without it there is nothing a
	//: licence could be matched against, and falling back to the NAME would
	//: make a freed handle a way in.
	if claims.RepositoryOwnerID == "" {
		//: Refuse a token with no immutable owner.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_claims"),
			errs.String("condition", "no immutable owner id to match an entitlement against"))
	}
	//: Timing last: the cheap structural checks run first.
	return checkTiming(claims, now)
}

// VerifyActionsToken authenticates a GitHub Actions OIDC token and returns what
// it asserts.
//
// keys are the JWKS entries fetched from GitHub. Passing them in rather than
// fetching here keeps this function offline and total: every refusal below is
// a property of the token, so a test can cover them without a network, and the
// fetch's own failure modes stay where the other network code lives.
//
// The order is deliberate: structure, then algorithm, then signature, then
// claims. Nothing about the payload is trusted until the signature over it has
// verified — reading a claim first and acting on it is how a forged token gets
// to choose what it is checked against.
func VerifyActionsToken(raw string, keys map[string]*rsa.PublicKey, audience string, now time.Time) (claims *ActionsClaimsValue, err error) {
	signingInput, header, payload, signature, splitErr := jwtSegments(raw)
	//: A token we cannot split cannot be authenticated.
	if splitErr != nil {
		//: Propagate the structural failure.
		return nil, splitErr
	}

	kid, headerErr := checkHeader(header)
	//: A header that fails any of its checks makes verification unsound.
	if headerErr != nil {
		//: Propagate the header failure.
		return nil, headerErr
	}

	key, known := keys[kid]
	//: An unknown kid is a rotated-out or a forged key, and this refuses
	//: either way. There is deliberately NO retry here and no caller that
	//: performs one — an earlier comment claimed one did, which was the
	//: promise-without-mechanism this audit is about. Trying every key we hold
	//: instead would widen the forgery surface for no benefit, and a second
	//: fetch is argued against where the fetch lives, in publishedJWKS.
	if !known {
		//: Refuse a token we hold no key for.
		return nil, refuse(coreent.ErrCIUnknownKey,
			errs.String("stage", "select_key"),
			errs.String("condition", "the key set publishes no entry with this kid"),
			errs.String("kid", kid))
	}
	//: Every way the selected key can be unusable, in one place.
	if keyErr := usableRSAKey(key, kid); keyErr != nil {
		//: Refuse rather than verify against something unusable.
		return nil, keyErr
	}

	//: Authenticate BEFORE decoding the payload: a forged token must not
	//: reach the JSON parser, let alone the authorization decision.
	if sigErr := verifySignature(signingInput, signature, key); sigErr != nil {
		//: Propagate the authentication failure.
		return nil, sigErr
	}

	//: Only now is the payload worth reading.
	decoded, decodeErr := strictUnmarshal[ActionsClaimsValue](payload, "token payload")
	//: A payload we cannot decode cannot be acted on.
	if decodeErr != nil {
		//: Propagate the decode failure.
		return nil, decodeErr
	}
	//: Authentic is not the same as addressed to us.
	if claimErr := checkClaims(&decoded, audience, now); claimErr != nil {
		//: Propagate the claim failure.
		return nil, claimErr
	}
	//: A token GitHub signed, minted for us, inside its window.
	return &decoded, nil
}

// jwkNumbers decodes the two big-endian values that make up an RSA public key.
//
// JWK requires the minimal big-endian encoding, so a leading zero byte means
// the value has two spellings — and two encodings of one number is one too
// many for something a trust decision rests on.
func jwkNumbers(entry *JWKValue) (modulus, exponent []byte, err error) {
	modulus, modErr := base64.RawURLEncoding.DecodeString(entry.Modulus)
	//: A modulus we cannot decode is not a key.
	if modErr != nil {
		//: Refuse the malformed entry.
		return nil, nil, classify(coreent.ErrCIUnverifiable, modErr,
			errs.String("stage", "decode_key"),
			errs.String("part", "modulus"),
			errs.String("kid", entry.KeyID))
	}
	exponent, expErr := base64.RawURLEncoding.DecodeString(entry.Exponent)
	//: An exponent we cannot decode is not a key.
	if expErr != nil {
		//: Refuse the malformed entry.
		return nil, nil, classify(coreent.ErrCIUnverifiable, expErr,
			errs.String("stage", "decode_key"),
			errs.String("part", "exponent"),
			errs.String("kid", entry.KeyID))
	}
	//: Empty or non-minimal encodings are refused rather than normalised.
	if len(modulus) == 0 || modulus[0] == 0 || len(exponent) == 0 || exponent[0] == 0 {
		//: Refuse a non-canonical encoding.
		return nil, nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_key"),
			errs.String("condition", "a value with a leading zero byte has two spellings"),
			errs.String("kid", entry.KeyID))
	}
	//: Two values that can be read as one number each.
	return modulus, exponent, nil
}

// jwkExponent turns the decoded exponent into the int rsa.PublicKey wants.
//
// RSA public exponents are small and odd: an even one shares a factor with
// every Euler totient, so it cannot be one, and a huge one is either broken or
// an attempt to make verification expensive.
func jwkExponent(raw []byte, kid string) (exponent int, err error) {
	value := new(big.Int).SetBytes(raw)
	//: Outside the sane range is either broken or hostile. The upper bound is
	//: not cosmetic: int is 32 bits on some platforms, so a larger value
	//: would silently truncate and rebuild a DIFFERENT key from the one
	//: published.
	if !value.IsInt64() || value.Int64() < minRSAExponent ||
		value.Int64() > maxRSAExponent || value.Int64()%evenDivisor == 0 {
		//: Refuse an unusable exponent.
		return 0, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "decode_key"),
			errs.String("condition", "an exponent outside the range RSA allows"),
			errs.String("kid", kid))
	}
	//: A small odd exponent.
	return int(value.Int64()), nil
}

// signsRS256 reports whether a published entry is allowed to verify RS256.
//
// "use" and "alg" are optional in JWK, so an entry that declares neither is
// accepted: GitHub has published both shapes. What is refused is an entry that
// declares something ELSE, since a key published for encryption or for another
// algorithm is being repurposed for a job it was not published for.
func signsRS256(entry *JWKValue) bool {
	//: An entry must be RSA, and must not contradict signing with RS256.
	return entry.KeyType == "RSA" &&
		(entry.Use == "" || entry.Use == "sig") &&
		(entry.Algorithm == "" || entry.Algorithm == "RS256")
}

// rsaKeyFromJWK builds a public key from a JWKS entry, refusing anything that
// is not a usable RS256 signing key.
func rsaKeyFromJWK(entry *JWKValue) (key *rsa.PublicKey, err error) {
	//: Only RSA keys can verify RS256, and only signing keys should be used
	//: for it: an entry marked for encryption is being repurposed.
	if !signsRS256(entry) {
		//: Refuse a key that is not an RS256 signing key.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "build_key"),
			errs.String("condition", "not published as an RS256 signing key"),
			errs.String("kid", entry.KeyID))
	}

	modulus, exponentBytes, numbersErr := jwkNumbers(entry)
	//: An entry whose numbers do not decode is not a key.
	if numbersErr != nil {
		//: Propagate the decode failure.
		return nil, numbersErr
	}
	exponent, expErr := jwkExponent(exponentBytes, entry.KeyID)
	//: An unusable exponent cannot verify anything.
	if expErr != nil {
		//: Propagate the exponent failure.
		return nil, expErr
	}

	built := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}
	//: A key too small to be GitHub's has been substituted somewhere — and one
	//: too large is not a key either: rsa.VerifyPKCS1v15 does modular
	//: arithmetic over whatever it is handed, so an absurd modulus turns every
	//: verification into a long computation, decided by a document nothing has
	//: authenticated yet.
	if bits := built.N.BitLen(); bits < minRSAModulusBits || bits > maxRSAModulusBits {
		//: Refuse a key outside the plausible range.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "build_key"),
			errs.String("condition", "a modulus outside the sizes a real signing key has"),
			errs.String("kid", entry.KeyID),
			errs.Int("bits", bits))
	}
	//: A usable RS256 verification key.
	return built, nil
}

// usableRSAKey reports why the key selected by kid cannot verify a signature, or
// nil when it can.
//
// The kid comes from the TOKEN, which is attacker text, so a map entry that is
// nil or carries no modulus must refuse rather than be dereferenced — a
// malformed key set a caller built would otherwise become a process panic
// instead of an unverifiable token.
func usableRSAKey(key *rsa.PublicKey, kid string) error {
	//: Nothing to verify against.
	if key == nil || key.N == nil {
		//: Refuse as unverifiable.
		return refuse(coreent.ErrCIUnknownKey,
			errs.String("stage", "select_key"),
			errs.String("condition", "the selected entry yielded no usable key"),
			errs.String("kid", kid))
	}
	//: A key too small to be the issuer's has been substituted somewhere.
	if key.N.BitLen() < minRSAModulusBits {
		//: Refuse an undersized key.
		return refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "select_key"),
			errs.String("condition", "the selected key is outside the sizes a real signing key has"),
			errs.String("kid", kid),
			errs.Int("bits", key.N.BitLen()))
	}

	//: The key can be used.
	return nil
}
