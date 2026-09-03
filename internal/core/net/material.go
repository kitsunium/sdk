// Package net — TLS material parsing helpers shared by NewIdentity.
package net

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// minAcceptedVersion is the oldest TLS version the domain will serve or dial.
// TLS 1.0 and 1.1 are deprecated by RFC 8996 and are refused, not warned about.
const minAcceptedVersion uint16 = tls.VersionTLS12

// parseKeyPair turns a PEM certificate chain and key into a tls.Certificate.
// Both halves empty is legitimate — a client that only verifies the server holds
// no identity of its own — but supplying one without the other is a mistake that
// would otherwise surface as a confusing handshake failure much later.
func parseKeyPair(certPEM, keyPEM []byte) ([]tls.Certificate, error) {
	//: neither half present: the caller holds no certificate, which is valid.
	if len(certPEM) == 0 && len(keyPEM) == 0 {
		return nil, nil
	}
	//: exactly one half present is always a configuration mistake.
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "cert_key_pair"),
			errs.String("why", "certificate and key must be supplied together"))
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		//: the message is not attached verbatim — it can quote key bytes.
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "cert_key_pair"),
			errs.String("why", "the certificate and key could not be parsed or do not match"))
	}
	return []tls.Certificate{pair}, nil
}

// parsePool builds a certificate pool from a PEM bundle.
//
// This is the trap the domain exists to close. x509.CertPool.AppendCertsFromPEM
// reports success through a boolean that is almost universally discarded, so a
// bundle that is empty, truncated, or accidentally a private key produces a pool
// that parses fine and verifies nothing — the failure then shows up as a trusted
// connection to an untrusted peer. Here a bundle that yields no certificate is a
// typed error, and an absent bundle is distinct from an unusable one.
func parsePool(bundlePEM []byte, field string) (*x509.CertPool, error) {
	//: no bundle means "use the platform trust store", signalled by a nil pool.
	if len(bundlePEM) == 0 {
		return nil, nil
	}
	pool := x509.NewCertPool()
	//: the discarded return value of AppendCertsFromPEM is the whole point.
	if !pool.AppendCertsFromPEM(bundlePEM) {
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", field),
			errs.String("why", "the PEM bundle contained no usable certificate"))
	}
	return pool, nil
}

// resolveMinVersion applies the domain default and refuses deprecated versions.
func resolveMinVersion(v uint16) (uint16, error) {
	//: zero means "unset" — apply the modern default rather than the stdlib's.
	if v == 0 {
		return defaultMinVersion, nil
	}
	//: TLS 1.0/1.1 are deprecated by RFC 8996; refuse rather than warn.
	if v < minAcceptedVersion {
		return 0, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "min_version"),
			errs.String("why", "TLS versions below 1.2 are refused"))
	}
	return v, nil
}
