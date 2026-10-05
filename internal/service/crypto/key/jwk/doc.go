// Package jwk — the bridge between a JWK and the key encodings the SDK's own
// schemes already speak, so adopting the format costs no re-encoding by hand:
//
//	service/crypto/sign/ecdsasig    PKIX DER public / SEC1 DER private
//	service/crypto/sign/ed25519sig  raw 32-octet public / raw 64-octet private
//	service/crypto/mac/hmacsha2     core/crypto.Key (redacting, 32 octets)
//
// The out-bound accessors named *Private / Secret hand back live key material,
// exactly as core/crypto.Key.Bytes() does, and carry the same rule: they feed a
// scheme, never a log line. They are in-process handoffs — the serialisation
// guard is MarshalPublic / MarshalPrivate, one layer up.
//
// Package jwk — curve arithmetic and the two validity checks a JWK parser is
// most often missing: that the declared point is actually ON the declared
// curve, and that a declared private scalar actually derives that point.
//
// Both go through crypto/ecdh rather than crypto/elliptic: ecdh.Curve's
// NewPublicKey / NewPrivateKey validate their input (on-curve, not the identity,
// scalar in [1, n-1]) whereas the elliptic.Curve point methods are deprecated
// precisely because they do not.
//
// Package jwk implements the RFC 7517 JSON Web Key representation for the key
// types the SDK's crypto domain already ships: EC (NIST P-256/P-384/P-521),
// OKP (Ed25519) and oct (symmetric). It is stdlib-only — a JWK is JSON plus
// base64url plus curve arithmetic, all of which crypto/ecdh, crypto/ed25519,
// crypto/x509 and encoding/json already provide — so it adds no dependency to
// internal/service.
//
// It is a FORMAT, not a connector: nothing here fetches a JWK Set over HTTP,
// caches one, or follows an OpenID discovery document. Bytes in, bytes out.
//
// # Exporting a private key is opt-in, never the default
//
// core/crypto.Key redacts itself precisely so key material cannot fall out of a
// log line, and a JWK serialiser is by construction a function that turns
// protected material into JSON. So the two paths are named, not flagged:
// MarshalPublic emits public members only and is what MarshalJSON — the path
// encoding/json takes by itself — delegates to, while MarshalPrivate is the
// only call that can emit "d" or "k", and reads as such at the call site. A
// symmetric key has no public half at all, so MarshalPublic refuses it with
// NoPublicForm rather than quietly publishing the secret (ADR 0030: the default
// must not be the dangerous one).
//
// KeyValue redacts under %v / %s / %#v the same way core/crypto.Key does: the
// rendering names kty, crv, kid and whether private material is present, and
// never a byte of the material itself.
//
// Package jwk — the encode half, and the place the package's one real security
// decision lives.
//
// Serialising a key is the operation that undoes core/crypto.Key's redaction:
// it turns protected material into JSON somebody will write to a file, a
// config map, or an HTTP response. So the two directions are not a boolean
// argument — they are two differently named methods, and the one the language
// reaches for on its own (MarshalJSON, i.e. plain json.Marshal) is the safe
// one. Emitting "d" or "k" requires typing MarshalPrivate at the call site,
// where a reviewer reads it.
//
// Package jwk — the decode half: JSON document to KeyValue, with every
// RFC 7517 / 7518 / 8037 rejection made explicit and typed.
//
// Parsing accepts private members. That is not the asymmetry this package
// guards: a caller parsing its own private JWK already holds the material. The
// guarded direction is serialisation (see marshal.go), where a protected value
// would become publishable JSON.
//
// There is deliberately no KeyValue.UnmarshalJSON. Parse is the single entry
// point, so there is exactly one place where a document becomes a key and
// exactly one set of checks it must pass — and KeyValue keeps a uniform value
// receiver, which is what makes MarshalJSON fire on a plain (non-pointer) key
// and therefore what makes the safe default actually reachable.
//
// Package jwk — the JWK Set (RFC 7517 §5) and, with it, the package's second
// deliberate refusal.
//
// A set is how key rotation is published: for a window, the old and the new key
// are both in the document. RFC 7517 §4.5 only SHOULD-s distinct "kid" values,
// so a set holding two keys under one kid is legal and does happen. Resolving
// it by taking the first match would make the answer depend on JSON member
// order — an ordering no RFC guarantees and no publisher promises to keep.
//
// So ByKid refuses an ambiguous lookup (AmbiguousKid) and AllByKid is the
// rotation path: it returns every candidate, in document order, and the caller
// decides — typically by trying each until a signature verifies. Same principle
// as ADR 0031: where any SDK-chosen answer would be arbitrary, refuse instead
// of guessing quietly.
//
// Package jwk — the RFC 7517 JSON wire shapes and the base64url codec every
// member goes through. Kept apart from the value type so nothing in jwk.go has
// an exported-looking JSON surface: keyJSON is the ONLY struct with json tags,
// and it is unexported, so encoding/json can never reach a KeyValue by
// reflection.
package jwk
