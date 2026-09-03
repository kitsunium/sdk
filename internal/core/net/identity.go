// Package net — the opaque, redacting TLS identity value.
package net

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultMinVersion is the floor applied when IdentityParams.MinVersion is zero.
// TLS 1.3 is the default rather than 1.2 because a new deployment has no legacy
// peer to accommodate; a caller that genuinely needs 1.2 must say so explicitly.
const defaultMinVersion uint16 = tls.VersionTLS12 + 1 // tls.VersionTLS13

// IdentityValue is an opaque TLS identity: a certificate chain, the trust
// anchors used to verify the peer, and the negotiated-version policy. Its String
// and GoString output is always "<redacted>" so an accidental %v, %s or %#v can
// never spill key material into a log line. The only way out is ClientConfig or
// ServerConfig, which mint a fresh *tls.Config per call.
//
// The zero value is valid and means "no identity" — IsZero reports it.
type IdentityValue struct {
	// certs holds the parsed keypair; empty for a client that only verifies.
	certs []tls.Certificate
	// roots verifies the peer; nil means "use the platform trust store".
	roots *x509.CertPool
	// clientCAs verifies client certificates on a server identity.
	clientCAs *x509.CertPool
	// serverName overrides the verified peer name.
	serverName string
	// minVersion is the resolved TLS floor, never zero after construction.
	minVersion uint16
	// nextProtos is the ALPN list.
	nextProtos []string
	// requireClientCert promotes a server identity to mutual TLS.
	requireClientCert bool
}

// String implements fmt.Stringer and always redacts so key material never
// reaches a log line through %v or %s.
func (i IdentityValue) String() string {
	//: constant redaction marker regardless of the material held.
	return "<redacted>"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump the unexported fields.
func (i IdentityValue) GoString() string {
	//: same constant marker — %#v must never expose TLS material.
	return "<redacted>"
}

// IsZero reports whether the identity carries no material at all, which is the
// zero value a caller gets when no TLS was configured.
func (i IdentityValue) IsZero() bool {
	//: minVersion is always set by NewIdentity, so it discriminates the zero value.
	return i.minVersion == 0 && len(i.certs) == 0 && i.roots == nil && i.clientCAs == nil
}

// ClientConfig returns a fresh *tls.Config for dialling a peer. Each call yields
// a new value so a caller mutating the result cannot affect any other user of
// the same identity.
func (i IdentityValue) ClientConfig() *tls.Config {
	//: a zero identity still yields a usable, safe-by-default client config.
	return &tls.Config{
		Certificates: i.certs,
		RootCAs:      i.roots,
		ServerName:   i.serverName,
		MinVersion:   i.resolvedMinVersion(),
		NextProtos:   i.nextProtos,
	}
}

// ServerConfig returns a fresh *tls.Config for accepting connections, switching
// to mutual TLS when the identity requires a client certificate.
func (i IdentityValue) ServerConfig() *tls.Config {
	cfg := &tls.Config{
		Certificates: i.certs,
		ClientCAs:    i.clientCAs,
		MinVersion:   i.resolvedMinVersion(),
		NextProtos:   i.nextProtos,
	}
	//: mutual TLS means the handshake fails unless the peer chains to clientCAs.
	if i.requireClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg
}

// resolvedMinVersion never returns zero, so a zero-value identity still pins a
// modern floor instead of inheriting the stdlib default.
func (i IdentityValue) resolvedMinVersion() uint16 {
	//: the zero value carries no explicit floor — apply the domain default.
	if i.minVersion == 0 {
		return defaultMinVersion
	}
	return i.minVersion
}

// NewIdentity validates in-memory TLS material and returns the opaque identity.
// It fails loudly where the standard library is silent: a CA bundle that yields
// no usable certificate is TLSMaterialInvalid, never an empty pool that would
// verify nothing.
func NewIdentity(p IdentityParams) (IdentityValue, error) {
	certs, err := parseKeyPair(p.CertPEM, p.KeyPEM)
	if err != nil {
		return IdentityValue{}, err
	}
	roots, err := parsePool(p.RootsPEM, "roots")
	if err != nil {
		return IdentityValue{}, err
	}
	clientCAs, err := parsePool(p.ClientCAPEM, "client_ca")
	if err != nil {
		return IdentityValue{}, err
	}
	minVersion, err := resolveMinVersion(p.MinVersion)
	if err != nil {
		return IdentityValue{}, err
	}
	//: a server demanding a client certificate it cannot verify would reject every
	//: peer at handshake time; refuse the configuration instead of the traffic.
	if p.RequireClientCert && clientCAs == nil {
		return IdentityValue{}, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "client_ca"),
			errs.String("why", "mutual TLS requires a client CA bundle"))
	}
	return IdentityValue{
		certs:             certs,
		roots:             roots,
		clientCAs:         clientCAs,
		serverName:        p.ServerName,
		minVersion:        minVersion,
		nextProtos:        p.NextProtos,
		requireClientCert: p.RequireClientCert,
	}, nil
}
