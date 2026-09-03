// Package net — the on-disk TLS material description.
package net

// IdentityFileParams names TLS material to read from the filesystem. Every path
// is optional on its own; the combination is validated once the bytes are in
// hand, by the same core constructor that validates in-memory material.
//
// It is the disk-sourced twin of IdentityParams and lives beside it on purpose:
// the two describe one domain concept — the material an identity is built from
// — and splitting them across layers would let the rules that govern them drift
// apart, which is exactly what this domain refuses for TLS.
type IdentityFileParams struct {
	// CertFile is the PEM leaf certificate followed by any intermediates.
	// Supplying it without KeyFile is always a configuration slip, never an
	// intention, and is refused: an mTLS client that silently degrades to plain
	// TLS is a security hole that reports nothing.
	CertFile string `json:"cert_file"`
	// KeyFile is the PEM private key matching CertFile.
	KeyFile string `json:"key_file"`
	// RootsFile is the PEM CA bundle used to verify the peer. Leaving it empty
	// selects the platform trust store, which is right for a public endpoint and
	// wrong for a private PKI.
	RootsFile string `json:"roots_file"`
	// ClientCAFile is the PEM CA bundle a server accepts client certificates
	// from. Required whenever RequireClientCert is set.
	ClientCAFile string `json:"client_ca_file"`
	// ServerName overrides the name verified against the peer certificate.
	//
	// This is far more often required than its "optional" typing suggests. Any
	// deployment reached through a port forward or a tunnel connects to
	// 127.0.0.1 while the peer certificate names the real service, and the
	// handshake then fails every single time until ServerName is set. Treat it
	// as mandatory whenever the dial address is not the certificate's name.
	ServerName string `json:"server_name"`
	// MinVersion is the lowest accepted TLS version. Zero selects TLS 1.3;
	// anything below TLS 1.2 is refused.
	MinVersion uint16 `json:"min_version"`
	// NextProtos is the ALPN protocol list, e.g. {"h2", "http/1.1"}.
	NextProtos []string `json:"next_protos"`
	// RequireClientCert turns a server identity into mutual TLS.
	RequireClientCert bool `json:"require_client_cert"`
}
