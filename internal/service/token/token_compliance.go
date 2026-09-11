// Package token — hosts compile-time interface assertions
// — keeping them out of the production
// source so the runtime binary carries no diagnostic-only declarations.
package token

import coretoken "github.com/kitsunium/sdk/internal/core/token"

// Compile-time assertions that every implementation satisfies the contract it
// claims — the build fails before any test runs if an interface and one of its
// implementations drift apart. Collecting them makes the set greppable: every
// type that must satisfy a port is named here, so a port that quietly lost an
// implementation is one diff away from being visible. hs256Binding is asserted
// against BOTH halves on purpose — HMAC is symmetric, so a verifier can also
// mint, and that is the property a caller should have to notice.
var (
	_ signingKey         = hs256Binding{}
	_ verifyingKey       = hs256Binding{}
	_ signingKey         = es256Signing{}
	_ verifyingKey       = es256Verifying{}
	_ signingKey         = ed25519Signing{}
	_ verifyingKey       = ed25519Verifying{}
	_ claimShape         = joseShape{}
	_ claimShape         = pasetoShape{}
	_ coretoken.Issuer   = (*jwsIssuer)(nil)
	_ coretoken.Issuer   = (*pasetoIssuer)(nil)
	_ coretoken.Verifier = (*jwsVerifier)(nil)
	_ coretoken.Verifier = (*pasetoVerifier)(nil)
	_ coretoken.Verifier = (*setVerifier)(nil)
)
