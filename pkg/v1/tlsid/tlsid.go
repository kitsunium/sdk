//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/tlsid .

// Package tlsid builds TLS and mutual-TLS identities for the SDK's network
// domain (ADR 0029), from files on disk or from material already in memory.
//
// An Identity is opaque and redacts itself: its String and GoString output is
// always "<redacted>", so an accidental %v, %s or %#v cannot spill key material
// into a log line. The only way out is [Identity.ClientConfig] or
// [Identity.ServerConfig], each of which mints a fresh *tls.Config, so one
// caller's mutation can never reach another.
//
// The package exists to close a specific, widely-repeated bug. The standard
// library's x509.CertPool.AppendCertsFromPEM reports failure through a boolean
// return that is almost universally discarded, so a CA bundle that is empty,
// truncated, or accidentally a private key produces a pool that parses without
// complaint and verifies nothing. Here such a bundle is a typed
// TLS_MATERIAL_INVALID error. An absent bundle (use the platform trust store)
// stays deliberately distinct from an unusable one.
//
// The same principle governs the rest of the surface: a certificate supplied
// without its key is refused rather than degraded to an anonymous connection, a
// server that demands a client certificate without a CA bundle to verify it
// against is refused at construction rather than rejecting every peer at
// handshake time, and the minimum version defaults to TLS 1.3 with TLS 1.0 and
// 1.1 refused outright per RFC 8996. A silent downgrade is always the wrong
// answer for transport security, so this package never performs one.
//
// # Loading from disk
//
//	id, err := tlsid.Load(tlsid.FileParams{
//		CertFile:   "/etc/pki/client.crt",
//		KeyFile:    "/etc/pki/client.key",
//		RootsFile:  "/etc/pki/ca.crt",
//		ServerName: "sdm.core.svc",
//	})
//	if err != nil {
//		return err
//	}
//	conn, err := tls.Dial("tcp", "127.0.0.1:8000", id.ClientConfig())
//
// ServerName is optional in the type and close to mandatory in practice: any
// deployment reached through a port forward or a tunnel dials 127.0.0.1 while
// the peer certificate names the real service, and the handshake fails until
// ServerName overrides the verified name.
//
// # Loading from memory
//
// New takes the same material as bytes, so certificates can come from a secret
// manager without ever touching a filesystem:
//
//	id, err := tlsid.New(tlsid.Params{CertPEM: cert, KeyPEM: key, RootsPEM: ca})
//
// # Mutual TLS
//
// Set RequireClientCert on a server identity and supply the CA bundle that
// client certificates must chain to:
//
//	id, err := tlsid.Load(tlsid.FileParams{
//		CertFile:          "/etc/pki/server.crt",
//		KeyFile:           "/etc/pki/server.key",
//		ClientCAFile:      "/etc/pki/client-ca.crt",
//		RequireClientCert: true,
//	})
//	ln := tls.NewListener(raw, id.ServerConfig())
package tlsid

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	svctlsid "github.com/kitsunium/sdk/internal/service/net/tlsid"
)

// Identity is an opaque TLS identity. Its String and GoString output is always
// "<redacted>"; ClientConfig and ServerConfig are the only ways to reach the
// material, and each returns a fresh *tls.Config.
type Identity = corenet.IdentityValue

// Params carries TLS material already held in memory.
type Params = corenet.IdentityParams

// FileParams names TLS material to read from the filesystem.
type FileParams = svctlsid.FileParams

// Sentinels returned by this package. Match with errors.Is or errs.HasCode.
var (
	// MaterialInvalid reports TLS material that is missing, unreadable,
	// malformed, or that yields no usable certificate. It is never downgraded to
	// an empty trust store.
	MaterialInvalid = corenet.TLSMaterialInvalid
	// HandshakeFailed reports a TLS or mutual-TLS handshake that did not complete.
	HandshakeFailed = corenet.TLSHandshakeFailed
)

// New validates TLS material held in memory and returns the opaque identity. It
// applies exactly the same rules as Load; only the source of the bytes differs.
func New(p Params) (Identity, error) {
	//: validation lives in core so memory- and disk-sourced material match.
	return corenet.NewIdentity(p)
}

// Load reads the named TLS material from disk and returns the opaque identity.
// A configured-but-unreadable file is an error, never a silently skipped one.
func Load(p FileParams) (Identity, error) {
	//: the service layer does the I/O and delegates every rule back to core.
	return svctlsid.Load(p)
}
