package consensus

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/kitsunium/sdk/internal/service/net/tlsid"
)

// PEMIdentity is a node identity read from PEM files: the certificate (its
// chain may follow it), the key, and the consensus CA. It is the
// net/tlsid reloadable identity of ADR 0174 D1: GetCertificate rereads the
// files when they change, and keeps the old identity when the new one
// fails, does not match its key, or carries another key or SAN — a renewal
// reuses the key, so the members' pins hold (R8-07). A new CA file is added
// to the trusted roots, so a rollover trusts the union.
type PEMIdentity struct {
	CertFile, KeyFile, CAFile string

	id *tlsid.Identity
}

// LoadPEMIdentity reads the files once and returns the identity and the CA
// pool to pass as TransportConfig.Roots.
func LoadPEMIdentity(certFile, keyFile, caFile string) (*PEMIdentity, *x509.CertPool, error) {
	id, err := tlsid.LoadReloadable(tlsid.FilesConfig{CertFile: certFile, KeyFile: keyFile, RootsFile: caFile})
	if err != nil {
		return nil, nil, transportMisconfigured("CertFile", err.Error())
	}
	leaf, err := id.Leaf()
	if err != nil {
		return nil, nil, transportMisconfigured("CertFile", err.Error())
	}
	if _, _, err := parseSAN(leaf); err != nil {
		return nil, nil, transportMisconfigured("CertFile", "URI SAN")
	}
	return &PEMIdentity{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, id: id}, id.Roots(), nil
}

// GetCertificate returns the current certificate, reloading it in place when
// its files changed.
func (p *PEMIdentity) GetCertificate() (*tls.Certificate, error) { return p.id.Certificate() }

// ReloadFailures counts the reloads refused so far.
func (p *PEMIdentity) ReloadFailures() int { return int(p.id.ReloadFailures()) }

// NewTCPTransportFromPEM builds the shipped transport from PEM files, over
// the reloadable identity, its roots included.
func NewTCPTransportFromPEM(certFile, keyFile, caFile string, cfg TransportConfig) (*TCPTransport, error) {
	p, pool, err := LoadPEMIdentity(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	cfg.Certificate, cfg.GetCertificate, cfg.Roots = nil, p.GetCertificate, pool
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return newTCPTransport(cfg, p.id)
}
