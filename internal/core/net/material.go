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
func parseKeyPair(certPEM, keyPEM []byte) (certs []tls.Certificate, err error) {
	//: neither half present: the caller holds no certificate, which is valid.
	if len(certPEM) == 0 && len(keyPEM) == 0 {
		//: no identity of our own is a valid client posture.
		return nil, nil
	}

	//: exactly one half present is always a configuration mistake.
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		//: refuse rather than let mTLS quietly become plain TLS.
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "cert_key_pair"),
			errs.String("why", "certificate and key must be supplied together"))
	}
	pair, perr := tls.X509KeyPair(certPEM, keyPEM)
	//: the parse error is not attached verbatim — it can quote key bytes.
	if perr != nil {
		//: the cause is deliberately not attached — it can quote key bytes.
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "cert_key_pair"),
			errs.String("why", "the certificate and key could not be parsed or do not match"))
	}
	//: the pair parsed and matched.
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
func parsePool(bundlePEM []byte, field string) (pool *x509.CertPool, err error) {
	//: no bundle means "use the platform trust store", signalled by a nil pool.
	if len(bundlePEM) == 0 {
		//: a nil pool tells crypto/tls to use the platform trust store.
		return nil, nil
	}

	built := x509.NewCertPool()
	//: the discarded return value of AppendCertsFromPEM is the whole point.
	if !built.AppendCertsFromPEM(bundlePEM) {
		//: an empty pool would verify nothing while looking configured.
		return nil, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", field),
			errs.String("why", "the PEM bundle contained no usable certificate"))
	}
	//: at least one certificate was admitted, so the pool verifies something.
	return built, nil
}

// resolveMinVersion applies the domain default and refuses deprecated versions.
func resolveMinVersion(v uint16) (version uint16, err error) {
	//: zero means "unset" — apply the modern default rather than the stdlib's.
	if v == 0 {
		//: apply the domain default instead of the stdlib's older floor.
		return defaultMinVersion, nil
	}

	//: TLS 1.0/1.1 are deprecated by RFC 8996; refuse rather than warn.
	if v < minAcceptedVersion {
		//: refuse the deprecated version outright.
		return 0, wrapAs(TLSMaterialInvalid, nil,
			errs.String("field", "min_version"),
			errs.String("why", "TLS versions below 1.2 are refused"))
	}
	//: the requested floor is at or above the accepted minimum.
	return v, nil
}
