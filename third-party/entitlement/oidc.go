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
// until it expires, minutes later. Closing that needs a server-side nonce,
// which the offline verification model rules out; the window is bounded and
// the exposure is one free seat, so it is accepted and stated rather than
// papered over.
package entitlement

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"time"
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
	// RepositoryOwnerID is the entitlement key: a numeric account id that
	// survives renames and cannot be reused. RepositoryOwner is the display
	// name for the same account and must never be what a licence matches on —
	// a freed handle can be claimed by someone else.
	RepositoryOwnerID string `json:"repository_owner_id"`
	// RepositoryOwner is the account's current login, for diagnostics only.
	RepositoryOwner string `json:"repository_owner"`
	// Repository is "owner/name", for diagnostics and optional narrowing.
	Repository string `json:"repository"`
	// EventName is the workflow trigger. pull_request_target runs with the
	// base repository's identity while potentially executing code from a
	// fork, so a policy may want to refuse it.
	EventName string `json:"event_name"`
	// RunnerEnvironment distinguishes "github-hosted" from "self-hosted". A
	// self-hosted runner's token is cryptographically identical but says
	// nothing about the isolation of the machine that holds it.
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
		return fmt.Errorf("%w: audience is null", ErrCIUnverifiable)
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
		return fmt.Errorf("%w: audience is neither a string nor an array", ErrCIUnverifiable)
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
		return "", nil, nil, nil, fmt.Errorf("%w: token larger than %d bytes", ErrCIUnverifiable, maxTokenBytes)
	}
	parts := strings.Split(raw, ".")
	//: Exactly three non-empty segments; a JWS with fewer is not one, and an
	//: empty signature segment is the "alg: none" shape by another route.
	if len(parts) != jwtParts || parts[headerSegment] == "" ||
		parts[payloadSegment] == "" || parts[signatureSegment] == "" {
		//: Refuse a malformed token.
		return "", nil, nil, nil, fmt.Errorf("%w: not a compact JWS", ErrCIUnverifiable)
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
			return "", nil, nil, nil, fmt.Errorf("%w: segment %d is not base64url", ErrCIUnverifiable, i)
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
	// verifier at. They are decoded ONLY so their presence can be refused —
	// honouring either would let the token nominate its own trust anchor.
	JWKSetURL string `json:"jku"`
	// X509URL is refused for the same reason as JWKSetURL.
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
		return decoded, fmt.Errorf("%w: %s is not a JSON object", ErrCIUnverifiable, what)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	//: Unknown fields are tolerated: GitHub adds claims over time and a new
	//: one must not break verification. What is refused is a second document.
	if decodeErr := decoder.Decode(&decoded); decodeErr != nil {
		//: Refuse what we cannot decode.
		return decoded, fmt.Errorf("%w: %s is not valid JSON: %w", ErrCIUnverifiable, what, decodeErr)
	}
	//: More() only reports whether another VALUE follows, so `{...}]junk`
	//: slips past it. Requiring the next read to be EOF is what actually
	//: says "this input was one document and nothing else".
	if _, eofErr := decoder.Token(); !errors.Is(eofErr, io.EOF) {
		//: Refuse rather than act on the first of several.
		return decoded, fmt.Errorf("%w: %s carries trailing content", ErrCIUnverifiable, what)
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
		return fmt.Errorf("%w: unsupported alg %q", ErrCIUnverifiable, header.Algorithm)
	}
	//: A typ, when present, must say JWT.
	if header.Type != "" && !strings.EqualFold(header.Type, "JWT") {
		//: Refuse a token typed as something else.
		return fmt.Errorf("%w: unexpected typ %q", ErrCIUnverifiable, header.Type)
	}
	//: "crit" names extensions the verifier MUST understand. We implement
	//: none, so the only correct answer to any of them is to refuse.
	if len(header.Critical) != 0 {
		//: Refuse rather than ignore something declared critical.
		return fmt.Errorf("%w: unsupported critical extensions %v", ErrCIUnverifiable, header.Critical)
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
		return "", fmt.Errorf("%w: token nominates its own key source", ErrCIUnverifiable)
	}
	//: Without a kid there is nothing to select, and trying every key would
	//: turn a rotation into an accepted forgery surface.
	if header.KeyID == "" {
		//: Refuse an unaddressed token.
		return "", fmt.Errorf("%w: token carries no kid", ErrCIUnverifiable)
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
		return fmt.Errorf("%w: signature length %d does not match the key", ErrCIUnverifiable, len(signature))
	}
	digest := sha256.Sum256([]byte(signingInput))
	//: This is the step everything else exists to make meaningful.
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		//: Forged, tampered with, or signed by another key.
		return fmt.Errorf("%w: signature does not verify", ErrCIUnverifiable)
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
		return fmt.Errorf("%w: token expires before it was issued", ErrCIUnverifiable)
	}
	//: A window wider than GitHub ever issues is either not from Actions or
	//: is being replayed, whoever signed it.
	if expires.Sub(issued) > maxTokenLifetime {
		//: Refuse an over-wide window.
		return fmt.Errorf("%w: token lifetime exceeds %s", ErrCIUnverifiable, maxTokenLifetime)
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
		return fmt.Errorf("%w: token carries no exp/iat", ErrCIUnverifiable)
	}
	expires := time.Unix(claims.ExpiresAt, 0)
	issued := time.Unix(claims.IssuedAt, 0)
	//: Past its expiry, with a small allowance for clock disagreement.
	if now.After(expires.Add(clockSkew)) {
		//: Refuse an expired token.
		return fmt.Errorf("%w: token expired at %s", ErrCIUnverifiable, expires.UTC().Format(time.RFC3339))
	}
	//: Issued in the future beyond the skew: either a forgery or a clock too
	//: wrong to reason about.
	if issued.After(now.Add(clockSkew)) {
		//: Refuse a token from the future.
		return fmt.Errorf("%w: token issued in the future", ErrCIUnverifiable)
	}
	//: nbf is optional, but binding when present.
	if claims.NotBefore != 0 && time.Unix(claims.NotBefore, 0).After(now.Add(clockSkew)) {
		//: Refuse a token that is not usable yet.
		return fmt.Errorf("%w: token is not valid yet", ErrCIUnverifiable)
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
		return fmt.Errorf("%w: issuer %q is not %s", ErrCIUnverifiable, claims.Issuer, ActionsIssuer)
	}
	//: An empty expected audience would match a token carrying an empty one,
	//: turning the check off exactly where it matters. A caller with nothing
	//: to ask for is a programming error, not a permissive default.
	if audience == "" {
		//: Refuse rather than verify against nothing.
		return fmt.Errorf("%w: no audience to verify against", ErrCIUnverifiable)
	}
	//: A token minted for a cloud provider or a registry must not be
	//: replayable here.
	if !claims.Audience.contains(audience) {
		//: Refuse a token minted for something else.
		return fmt.Errorf("%w: token is not minted for %s", ErrCIUnverifiable, audience)
	}
	//: The subject is not parsed for ownership, but its absence means the
	//: token is not the shape Actions produces.
	if claims.Subject == "" {
		//: Refuse a subjectless token.
		return fmt.Errorf("%w: token carries no subject", ErrCIUnverifiable)
	}
	//: The owner id is the entitlement key. Without it there is nothing a
	//: licence could be matched against, and falling back to the NAME would
	//: make a freed handle a way in.
	if claims.RepositoryOwnerID == "" {
		//: Refuse a token with no immutable owner.
		return fmt.Errorf("%w: token carries no repository_owner_id", ErrCIUnverifiable)
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
	//: An unknown kid is a rotated or forged key. The caller refreshes the
	//: JWKS once and retries; trying every key we hold instead would widen
	//: the forgery surface for no benefit.
	if !known {
		//: Refuse a token we hold no key for.
		return nil, fmt.Errorf("%w: no published key for kid %q", ErrCIUnknownKey, kid)
	}
	//: A key too small to be GitHub's has been substituted somewhere.
	if key.N.BitLen() < minRSAModulusBits {
		//: Refuse an undersized key.
		return nil, fmt.Errorf("%w: signing key is %d bits", ErrCIUnverifiable, key.N.BitLen())
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
		return nil, nil, fmt.Errorf("%w: key %q has an undecodable modulus", ErrCIUnverifiable, entry.KeyID)
	}
	exponent, expErr := base64.RawURLEncoding.DecodeString(entry.Exponent)
	//: An exponent we cannot decode is not a key.
	if expErr != nil {
		//: Refuse the malformed entry.
		return nil, nil, fmt.Errorf("%w: key %q has an undecodable exponent", ErrCIUnverifiable, entry.KeyID)
	}
	//: Empty or non-minimal encodings are refused rather than normalised.
	if len(modulus) == 0 || modulus[0] == 0 || len(exponent) == 0 || exponent[0] == 0 {
		//: Refuse a non-canonical encoding.
		return nil, nil, fmt.Errorf("%w: key %q is not canonically encoded", ErrCIUnverifiable, entry.KeyID)
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
		return 0, fmt.Errorf("%w: key %q has an unusable exponent", ErrCIUnverifiable, kid)
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
		return nil, fmt.Errorf("%w: key %q is not an RS256 signing key", ErrCIUnverifiable, entry.KeyID)
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
		return nil, fmt.Errorf("%w: key %q is %d bits", ErrCIUnverifiable, entry.KeyID, bits)
	}
	//: A usable RS256 verification key.
	return built, nil
}
