// Package entitlement — the error-code range this domain owns (framework/connectors/ssh block).
//
// Package entitlement is the contract for machine entitlement: turning local
// key material plus a vendor-signed roster into a grant, or a typed refusal.
//
// The trust model has exactly one SIGNER: the vendor. What a consuming binary
// links in is that signer's ed25519 public key — or, while a signing key is
// being rotated, an ordered list of the keys it will accept, which is a property
// of the verifier and lives in framework/internal/service/entitlement. Several accepted
// keys are still one signer: authentication establishes that the vendor issued
// these bytes and nothing more, so no quorum is being taken. Everything else —
// the roster, the per-subject keys, the host serving them — is untrusted input. A
// hostile endpoint can serve whatever it likes; it cannot forge a signature made
// with the vendor's private half.
//
// What that anchor cannot do is survive a patched binary. A check running on
// someone else's machine is removable by definition. This domain stops casual
// sharing and makes revocation real for cooperative installs; it is not a wall
// against a determined attacker, and is deliberately not built as if it were.
//
// # Why Identity is a port
//
// Proving possession needs key material the user already has, in a format the
// standard library cannot parse — OpenSSH, via golang.org/x/crypto/ssh, which
// brings golang.org/x/sys and is banned SDK-wide (ADR 0078). Naming the
// capability as a port keeps the MECHANISM here, stdlib-only, and puts the one
// implementation that needs a vendor dependency where a vendor dependency is
// allowed: the framework's connector module framework/connectors/ssh, which a
// consumer opts into (ADR 0158).
//
// A consumer that does not want that dependency supplies its own Identity. That
// is not a theoretical escape hatch — the product this was versed from does
// exactly that, keeping its 327 lines of SSH handling rather than pulling the
// connector's graph for them.
//
// There is **no registry**. A registry's key would name an identity scheme, and
// the whole trust chain is anchored to one vendor for one product.
//
// Package entitlement — the sentinels every implementation of this domain
// returns.
//
// Each names a distinct operator situation rather than a generic failure,
// because callers map them to exit codes and an operator acts on the
// difference: a revoked subject, an expired window and an unreachable roster
// need three different responses.
//
// Package entitlement - the record of a verification that succeeded. A daemon
// ages against it: memory of a past check must not outlive the roster window,
// otherwise revocation would never reach a long-running process.
//
// Package entitlement - where the roster is fetched from. Authority comes from
// the vendor signature, never from the origin that served the bytes, so the
// binary is free to ask several places and keep whichever answers.
//
// Package entitlement - the signed roster: the only statement the binary trusts,
// and only because the vendor signed it. Origin is irrelevant here; a
// substituted endpoint can serve any bytes but cannot forge the signature.
package entitlement
