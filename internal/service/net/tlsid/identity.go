package tlsid

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// This file is the identity sibling of ADR 0174 D1: a TLS identity checked
// on every handshake, the verified peer handed to the caller, and a source
// reloaded in place. It sits beside Load, which builds the static
// corenet.IdentityValue; the two do not share a type because a reloadable
// identity is a live object, not a value.

// PeerIdentity is what a handshake PROVED about the peer: its verified leaf
// and chain, and the SHA-256 of its SubjectPublicKeyInfo, the pin a member
// is admitted on. Nothing in it comes from a frame the peer wrote.
type PeerIdentity struct {
	// Leaf is the peer's verified certificate.
	Leaf *x509.Certificate
	// Chain is the verified chain, leaf first.
	Chain []*x509.Certificate
	// SPKI is the SHA-256 of the leaf's SubjectPublicKeyInfo.
	SPKI [32]byte
	// URIs are the leaf's URI SANs.
	URIs []*url.URL
	// NotAfter is when the leaf expires: a connection is closed by then.
	NotAfter time.Time
}

// SPKI returns the pin of a certificate: the SHA-256 of its
// SubjectPublicKeyInfo.
func SPKI(c *x509.Certificate) [32]byte { return sha256.Sum256(c.RawSubjectPublicKeyInfo) }

// Verify is a caller's check of a verified peer, run on EVERY handshake,
// after the chain, the roots and the extended key usage passed. A non-nil
// error fails the handshake (fail-closed).
type Verify func(peer PeerIdentity) error

// Identity is a node's own TLS identity and the roots it trusts, reloadable
// in place. It is safe for concurrent use.
type Identity struct {
	get      func() (*tls.Certificate, error)
	roots    atomic.Pointer[x509.CertPool]
	failures atomic.Int64

	// For an identity read from files.
	mu        sync.Mutex
	files     *Files
	cur       *tls.Certificate
	spki      [32]byte
	san       string
	stamp     time.Time
	checked   time.Time
	rootsSeen [][]byte
}

// Files names the PEM files of a reloadable identity.
type Files struct {
	// CertFile holds the certificate, its chain may follow it.
	CertFile string
	// KeyFile holds its private key.
	KeyFile string
	// RootsFile holds the roots, trusted as RootCAs AND ClientCAs.
	RootsFile string
	// Every bounds how often the files are looked at; 0 is one second.
	Every time.Duration
}

func materialInvalid(field, why string) error {
	return errs.Wrap(corenet.TLSMaterialInvalid, errs.WrapParams{}, errs.String("field", field), errs.String("why", why))
}

// NewIdentity builds an identity over a certificate source and its roots;
// get is asked at every handshake. nil roots are refused: an identity that
// trusts the platform's roots trusts everybody's CA.
func NewIdentity(get func() (*tls.Certificate, error), roots *x509.CertPool) (*Identity, error) {
	if get == nil {
		return nil, materialInvalid("certificate", "nil source")
	}
	if roots == nil {
		return nil, materialInvalid("roots", "nil")
	}
	id := &Identity{get: get}
	id.roots.Store(roots)
	if _, err := id.Certificate(); err != nil {
		return nil, err
	}
	return id, nil
}

// LoadReloadable reads an identity from files and reloads it in place when
// they change. A reload is refused, counted, and the old identity kept, when
// the new certificate does not parse or match its key, or names another URI
// SAN, or carries another key: a renewal reuses the key, so the pins hold.
// A new roots file is added to the old roots, so a CA rollover trusts the
// union until the process restarts.
func LoadReloadable(f Files) (*Identity, error) {
	if f.Every <= 0 {
		f.Every = time.Second
	}
	id := &Identity{files: &f}
	c, leaf, err := readPair(f.CertFile, f.KeyFile)
	if err != nil {
		return nil, err
	}
	pemRoots, err := os.ReadFile(f.RootsFile)
	if err != nil {
		return nil, materialInvalid("roots_file", err.Error())
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemRoots) {
		return nil, materialInvalid("roots_file", "no certificate")
	}
	id.roots.Store(pool)
	id.rootsSeen = [][]byte{pemRoots}
	id.cur, id.spki, id.san = c, SPKI(leaf), sanOf(leaf)
	id.stamp = modTime(f.CertFile, f.KeyFile, f.RootsFile)
	id.checked = time.Now()
	id.get = id.reload
	return id, nil
}

func sanOf(c *x509.Certificate) string {
	s := ""
	for _, u := range c.URIs {
		s += u.String() + " "
	}
	for _, d := range c.DNSNames {
		s += d + " "
	}
	return s
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

func readPair(certFile, keyFile string) (*tls.Certificate, *x509.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		// LoadX509KeyPair also refuses a key that does not match the
		// certificate.
		return nil, nil, materialInvalid("cert_file", err.Error())
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, nil, materialInvalid("cert_file", err.Error())
	}
	c.Leaf = leaf
	return &c, leaf, nil
}

// reload is the file identity's source: at most every Files.Every it looks
// at the files, and swaps in a renewal that keeps the key and the SAN.
func (id *Identity) reload() (*tls.Certificate, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if time.Since(id.checked) < id.files.Every {
		return id.cur, nil
	}
	id.checked = time.Now()
	m := modTime(id.files.CertFile, id.files.KeyFile, id.files.RootsFile)
	if !m.After(id.stamp) {
		return id.cur, nil
	}
	c, leaf, err := readPair(id.files.CertFile, id.files.KeyFile)
	switch {
	case err != nil, SPKI(leaf) != id.spki, sanOf(leaf) != id.san:
		id.failures.Add(1)
	default:
		id.cur = c
	}
	if pemRoots, err := os.ReadFile(id.files.RootsFile); err == nil && !seen(id.rootsSeen, pemRoots) {
		pool := id.roots.Load().Clone()
		if pool.AppendCertsFromPEM(pemRoots) {
			id.rootsSeen = append(id.rootsSeen, pemRoots)
			id.roots.Store(pool)
		} else {
			id.failures.Add(1)
		}
	}
	id.stamp = m
	return id.cur, nil
}

func seen(all [][]byte, b []byte) bool {
	for _, x := range all {
		if string(x) == string(b) {
			return true
		}
	}
	return false
}

// Certificate returns the identity's current certificate.
func (id *Identity) Certificate() (*tls.Certificate, error) {
	c, err := id.get()
	if err != nil {
		return nil, materialInvalid("certificate", err.Error())
	}
	if c == nil || len(c.Certificate) == 0 {
		return nil, materialInvalid("certificate", "empty")
	}
	return c, nil
}

// Leaf returns the current certificate, parsed.
func (id *Identity) Leaf() (*x509.Certificate, error) {
	c, err := id.Certificate()
	if err != nil {
		return nil, err
	}
	if c.Leaf != nil {
		return c.Leaf, nil
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, materialInvalid("certificate", err.Error())
	}
	return leaf, nil
}

// Roots returns the trusted roots: the union of every roots file read.
func (id *Identity) Roots() *x509.CertPool { return id.roots.Load() }

// ReloadFailures counts the reloads refused so far.
func (id *Identity) ReloadFailures() int64 { return id.failures.Load() }

// verifyPeer runs the full x509 verification, roots and extended key usage,
// before the caller's check reads anything of the certificate (ADR 0174 D1).
func (id *Identity) verifyPeer(cs tls.ConnectionState, usage x509.ExtKeyUsage, check Verify) error {
	if len(cs.PeerCertificates) == 0 {
		return errs.Wrap(corenet.TLSHandshakeFailed, errs.WrapParams{}, errs.String("why", "no peer certificate"))
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: id.Roots(), Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{usage}})
	if err != nil {
		return errs.Wrap(corenet.TLSHandshakeFailed, errs.WrapParams{}, errs.String("why", "chain: "+err.Error()))
	}
	if check == nil {
		return nil
	}
	return check(PeerIdentity{Leaf: leaf, Chain: chains[0], SPKI: SPKI(leaf), URIs: leaf.URIs, NotAfter: leaf.NotAfter})
}

func (id *Identity) base() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Session tickets are off: a resumed session would skip the
		// certificate check, and every handshake must run it (ADR 0174 D1).
		SessionTicketsDisabled: true,
		GetCertificate:         func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return id.Certificate() },
		GetClientCertificate:   func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return id.Certificate() },
	}
}

// ServerConfig accepts only a client whose chain verifies against the roots
// with clientAuth, then passes check, on every handshake.
func (id *Identity) ServerConfig(check Verify) *tls.Config {
	c := id.base()
	c.ClientAuth = tls.RequireAnyClientCert
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		return id.verifyPeer(cs, x509.ExtKeyUsageClientAuth, check)
	}
	return c
}

// ClientConfig dials only a server whose chain verifies against the roots
// with serverAuth, then passes check, on every handshake. The standard
// verification is replaced by this one, not skipped: it would look for a
// DNS name, and an identity may name a URI only.
func (id *Identity) ClientConfig(check Verify) *tls.Config {
	c := id.base()
	c.InsecureSkipVerify = true
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		return id.verifyPeer(cs, x509.ExtKeyUsageServerAuth, check)
	}
	return c
}

// PeerOf returns the verified peer of a completed handshake, false before
// one or without a peer certificate.
func PeerOf(cs tls.ConnectionState) (PeerIdentity, bool) {
	if !cs.HandshakeComplete || len(cs.PeerCertificates) == 0 {
		return PeerIdentity{}, false
	}
	leaf := cs.PeerCertificates[0]
	p := PeerIdentity{Leaf: leaf, SPKI: SPKI(leaf), URIs: leaf.URIs, NotAfter: leaf.NotAfter}
	if len(cs.VerifiedChains) > 0 {
		p.Chain = cs.VerifiedChains[0]
	} else {
		p.Chain = cs.PeerCertificates
	}
	return p, true
}

// Deadline is when a connection to peer must end: at its certificate's
// NotAfter, or after maxAge from opened, whichever comes first, so a
// revoked or expired identity cannot ride an old connection.
func Deadline(peer PeerIdentity, opened time.Time, maxAge time.Duration) time.Time {
	d := opened.Add(maxAge)
	if !peer.NotAfter.IsZero() && peer.NotAfter.Before(d) {
		return peer.NotAfter
	}
	return d
}
