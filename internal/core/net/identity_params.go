// Package net — the TLS identity construction parameters.
package net

// IdentityParams carries in-memory TLS material. Every field is optional on its
// own; the combination is what is validated by NewIdentity. Sourcing material
// from memory rather than from disk is what lets a consumer pull certificates
// out of a secret manager without ever writing them to a filesystem.
type IdentityParams struct {
	// CertPEM is the PEM-encoded leaf certificate followed by any intermediates.
	// Paired with KeyPEM; supplying one without the other is an error.
	CertPEM []byte
	// KeyPEM is the PEM-encoded private key matching CertPEM.
	KeyPEM []byte
	// RootsPEM is the PEM-encoded CA bundle used to verify the peer. When empty
	// the platform trust store is used, which is the right default for a public
	// endpoint and the wrong one for a private PKI.
	RootsPEM []byte
	// ClientCAPEM is the PEM-encoded CA bundle a server accepts client
	// certificates from. It is meaningless on a client identity.
	ClientCAPEM []byte
	// ServerName overrides the name verified against the peer certificate. It is
	// required when connecting to a host by IP whose certificate names a DNS name.
	ServerName string
	// MinVersion is the lowest accepted TLS version. Zero selects TLS 1.3;
	// anything below TLS 1.2 is refused outright.
	MinVersion uint16
	// NextProtos is the ALPN protocol list, e.g. {"h2", "http/1.1"}.
	NextProtos []string
	// RequireClientCert turns a server identity into mutual TLS: the peer must
	// present a certificate that chains to ClientCAPEM.
	RequireClientCert bool
}
