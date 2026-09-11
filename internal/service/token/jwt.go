// Package token — the JWT constructor set: one per algorithm, each accepting
// only the Go key type its algorithm can use.
//
// That is not documentation. NewHS256Verifier's parameter is a
// core/crypto.Key — a struct with an unexported field — so the classic
// confusion bug, handing it an EC or RSA public key so the attacker can sign
// with the public half, is a call that does not compile.
package token

import (
	"crypto/ecdsa"
	"crypto/ed25519"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
)

// NewHS256Issuer returns a JWT issuer signing with HMAC-SHA-256 under secret.
//
// HS256 is symmetric: anybody who can verify these tokens can also mint them.
// Reach for [NewES256Issuer] or [NewEdDSAIssuer] the moment the verifier is
// not the same trust domain as the issuer.
func NewHS256Issuer(secret corecrypto.Key, cfg IssuerConfig) (issuer coretoken.Issuer, err error) {
	binding, berr := bindSecret(secret)
	//: the key is validated before an issuer exists to misuse it.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the issuer around the algorithm-bound key.
	return newJWSIssuer(binding, cfg)
}

// NewES256Issuer returns a JWT issuer signing with ECDSA P-256 + SHA-256,
// emitting the fixed-width R||S signature of RFC 7518 §3.4.
func NewES256Issuer(priv *ecdsa.PrivateKey, cfg IssuerConfig) (issuer coretoken.Issuer, err error) {
	binding, berr := bindP256Private(priv)
	//: curve and point are checked at construction.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the issuer around the algorithm-bound key.
	return newJWSIssuer(binding, cfg)
}

// NewEdDSAIssuer returns a JWT issuer signing with Ed25519 (JOSE "EdDSA").
func NewEdDSAIssuer(priv ed25519.PrivateKey, cfg IssuerConfig) (issuer coretoken.Issuer, err error) {
	binding, berr := bindEd25519Private(priv, coretoken.AlgorithmEdDSA)
	//: the key length is checked at construction, not at the stdlib's panic.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the issuer around the algorithm-bound key.
	return newJWSIssuer(binding, cfg)
}

// NewHS256Verifier returns a JWT verifier bound to HMAC-SHA-256 under secret.
func NewHS256Verifier(secret corecrypto.Key, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	binding, berr := bindSecret(secret)
	//: no verifier exists until the key is valid.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the verifier around the algorithm-bound key.
	return newJWSVerifier(binding, cfg)
}

// NewES256Verifier returns a JWT verifier bound to ECDSA P-256 under pub.
//
// pub is validated as a curve point, not merely measured: an off-curve point
// is a documented route to key recovery (RFC 8725 §3.4).
func NewES256Verifier(pub *ecdsa.PublicKey, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	binding, berr := bindP256Public(pub)
	//: no verifier exists until the key is valid.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the verifier around the algorithm-bound key.
	return newJWSVerifier(binding, cfg)
}

// NewEdDSAVerifier returns a JWT verifier bound to Ed25519 under pub.
func NewEdDSAVerifier(pub ed25519.PublicKey, cfg VerifierConfig) (verifier coretoken.Verifier, err error) {
	binding, berr := bindEd25519Public(pub, coretoken.AlgorithmEdDSA)
	//: no verifier exists until the key is valid.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	//: build the verifier around the algorithm-bound key.
	return newJWSVerifier(binding, cfg)
}
