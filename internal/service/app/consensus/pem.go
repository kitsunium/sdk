package consensus

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"sync"
	"time"
)

// PEMIdentity is a node identity read from PEM files: the certificate (its
// chain may follow it), the key, and the consensus CA. GetCertificate
// rereads the files when they change, and keeps the old identity when the
// new one fails, does not match its key, or carries another key or SAN: a
// renewal reuses the key, so the members' pins hold (R8-07).
type PEMIdentity struct {
	CertFile, KeyFile, CAFile string

	mu      sync.Mutex
	cur     *tls.Certificate
	stamp   time.Time
	spki    [32]byte
	san     string
	failed  int
	checked time.Time
}

// LoadPEMIdentity reads the files once and returns the identity and the CA
// pool to pass as TransportConfig.Roots.
func LoadPEMIdentity(certFile, keyFile, caFile string) (*PEMIdentity, *x509.CertPool, error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, nil, transportMisconfigured("CAFile", err.Error())
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, nil, transportMisconfigured("CAFile", "no certificate")
	}
	id := &PEMIdentity{CertFile: certFile, KeyFile: keyFile, CAFile: caFile}
	c, leaf, err := id.read()
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := parseSAN(leaf); err != nil {
		return nil, nil, transportMisconfigured("CertFile", "URI SAN")
	}
	id.cur, id.spki, id.san = c, spkiOf(leaf), leaf.URIs[0].String()
	id.stamp = modTime(certFile, keyFile)
	return id, pool, nil
}

func modTime(files ...string) time.Time {
	var t time.Time
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(t) {
			t = st.ModTime()
		}
	}
	return t
}

func (p *PEMIdentity) read() (*tls.Certificate, *x509.Certificate, error) {
	c, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
	if err != nil {
		return nil, nil, transportMisconfigured("CertFile", err.Error())
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, nil, transportMisconfigured("CertFile", err.Error())
	}
	c.Leaf = leaf
	return &c, leaf, nil
}

// GetCertificate returns the current certificate, reloading it in place when
// its files changed.
func (p *PEMIdentity) GetCertificate() (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.checked) < time.Second {
		return p.cur, nil
	}
	p.checked = time.Now()
	if m := modTime(p.CertFile, p.KeyFile); m.After(p.stamp) {
		c, leaf, err := p.read()
		switch {
		case err != nil, len(leaf.URIs) != 1, leaf.URIs[0].String() != p.san, spkiOf(leaf) != p.spki:
			p.failed++
		default:
			p.cur, p.stamp = c, m
		}
	}
	return p.cur, nil
}

// ReloadFailures counts the reloads refused so far.
func (p *PEMIdentity) ReloadFailures() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed
}

// NewTCPTransportFromPEM builds the shipped transport from PEM files.
func NewTCPTransportFromPEM(certFile, keyFile, caFile string, cfg TransportConfig) (*TCPTransport, error) {
	id, pool, err := LoadPEMIdentity(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	cfg.Certificate, cfg.GetCertificate, cfg.Roots = nil, id.GetCertificate, pool
	t, err := NewTCPTransport(cfg)
	if err != nil {
		return nil, err
	}
	t.reloads = id.ReloadFailures
	return t, nil
}
