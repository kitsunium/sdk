//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/tlsid .

// Package tlsid builds TLS and mutual-TLS identities for the SDK's network
// domain (ADR 0029), from files on disk or from material already in memory.
//
// An Identity is opaque and redacts itself: its String and GoString output is
// always "<redacted>", so an accidental %v, %s or %#v cannot spill key material
// into a log line. The only way out is [Identity].ClientConfig or
// [Identity].ServerConfig, each of which mints a fresh *tls.Config, so one
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
