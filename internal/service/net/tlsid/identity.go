package tlsid

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"os"
	"slices"
	"strings"
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

// PeerIdentityValue is what a handshake PROVED about the peer: its verified leaf
// and chain, and the SHA-256 of its SubjectPublicKeyInfo, the pin a member
// is admitted on. Nothing in it comes from a frame the peer wrote.
type PeerIdentityValue struct {
	// Leaf is the peer's verified certificate.
	Leaf *x509.Certificate
	// Chain is the chain the standard verifier built, leaf first; empty
	// when tls.ConnectionState.VerifiedChains is, as under the identity's
	// own VerifyConnection, which verifies the chain itself (see PeerOf).
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
type Verify func(peer PeerIdentityValue) error

// Identity is a node's own TLS identity and the roots it trusts, reloadable
// in place. It is safe for concurrent use.
type Identity struct {
	get      func() (*tls.Certificate, error)
	roots    atomic.Pointer[x509.CertPool]
	failures atomic.Int64

	// For an identity read from files. A look decides by the files' bytes,
	// never by their times (#633): pairLast and rootsLast are the digests of
	// the bytes the last look decided on, rootsSeen those of every roots
	// file merged.
	mu        sync.Mutex
	files     *FilesConfig
	cur       *tls.Certificate
	spki      [32]byte
	san       string
	pairLast  pairSum
	rootsLast [32]byte
	checked   time.Time
	rootsSeen [][32]byte
}

// pairSum is what a look compares a pair with: the SHA-256 of each file's
// bytes, kept apart so that bytes moved from one file to the other are a
// change too. A digest, not the bytes, so the key's PEM is not kept.
type pairSum struct{ cert, key [32]byte }

// sumPair is the pairSum of a certificate's and a key's bytes.
func sumPair(certPEM, keyPEM []byte) pairSum {
	return pairSum{cert: sha256.Sum256(certPEM), key: sha256.Sum256(keyPEM)}
}

// FilesConfig names the PEM files of a reloadable identity. They are looked
// at no more often than Every, and a renewal is swapped in only when it keeps
// the key and the URI SAN.
type FilesConfig struct {
	// CertFile holds the certificate, its chain may follow it.
	CertFile string
	// KeyFile holds its private key.
	KeyFile string
	// RootsFile holds the roots, trusted as RootCAs AND ClientCAs.
	RootsFile string
	// Every bounds how often the files are looked at; 0 is one second.
	Every time.Duration
}

// materialInvalid is the one refusal of unusable TLS material: it names the
// field at fault and why, and never the material itself.
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
func LoadReloadable(f FilesConfig) (*Identity, error) {
	if f.Every <= 0 {
		f.Every = time.Second
	}
	id := &Identity{files: &f}
	certPEM, err := os.ReadFile(f.CertFile)
	if err != nil {
		return nil, materialInvalid("cert_file", err.Error())
	}
	keyPEM, err := os.ReadFile(f.KeyFile)
	if err != nil {
		return nil, materialInvalid("key_file", err.Error())
	}
	c, leaf, err := parsePair(certPEM, keyPEM)
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
	id.rootsLast = sha256.Sum256(pemRoots)
	id.rootsSeen = [][32]byte{id.rootsLast}
	id.cur, id.spki, id.san = c, SPKI(leaf), sanOf(leaf)
	id.pairLast = sumPair(certPEM, keyPEM)
	id.checked = time.Now()
	id.get = id.reload
	return id, nil
}

// sanOf flattens a certificate's URI and DNS SANs into one comparable
// string, so a reload can refuse a renewal that names anything else.
func sanOf(c *x509.Certificate) string {
	var b strings.Builder
	for _, u := range c.URIs {
		b.WriteString(u.String())
		b.WriteByte(' ')
	}
	for _, d := range c.DNSNames {
		b.WriteString(d)
		b.WriteByte(' ')
	}
	return b.String()
}

// parsePair builds a certificate from its PEM and its key's, refusing a pair
// that does not match, and parses the leaf. It takes the very bytes a look
// compared, never the files read a second time.
func parsePair(certPEM, keyPEM []byte) (*tls.Certificate, *x509.Certificate, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// X509KeyPair also refuses a key that does not match the
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
//
// A look reads the three files and decides by their bytes, never by their
// times (#633). A copy that keeps its source's time (cp -p, rsync -a, tar
// x), a filesystem of a coarse granularity, or a clock set back, gives a new
// file a time older than, or the same as, the one a look last saw, and a
// rule of "newer than the last look" never read it: a CA rollover's bundle
// was never trusted, a renewal never served. The files are a few KiB of PEM,
// read at most once per Every; bytes the last look already decided on are
// not parsed again.
func (id *Identity) reload() (*tls.Certificate, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if time.Since(id.checked) < id.files.Every {
		return id.cur, nil
	}
	id.checked = time.Now()
	id.reloadPair()
	id.reloadRoots()
	return id.cur, nil
}

// reloadPair swaps in a pair whose bytes changed when it keeps the key and
// the SAN. A file that cannot be read is no change, so a transient absence,
// between an unlink and its replacement, neither swaps nor counts. A refused
// pair is counted once and read again at every look, so a key landing after
// its certificate is taken whatever the two files' times (review of PR
// #301, L3); bytes equal to the refused ones are not parsed again, since
// they would be refused again.
func (id *Identity) reloadPair() {
	certPEM, err := os.ReadFile(id.files.CertFile)
	if err != nil {
		return
	}
	keyPEM, err := os.ReadFile(id.files.KeyFile)
	if err != nil {
		return
	}
	sum := sumPair(certPEM, keyPEM)
	if sum == id.pairLast {
		return
	}
	id.pairLast = sum
	c, leaf, err := parsePair(certPEM, keyPEM)
	if err != nil || SPKI(leaf) != id.spki || sanOf(leaf) != id.san {
		id.failures.Add(1)
		return
	}
	id.cur = c
}

// reloadRoots adds a roots file whose bytes changed to the roots, which only
// grow until the process restarts (ADR 0174 D1, review of PR #301, L2). A
// file that cannot be read is no change; one that names no certificate,
// an empty one included, is counted once and never replaces nor narrows the
// roots. Bytes already merged do not grow the pool again.
func (id *Identity) reloadRoots() {
	pemRoots, err := os.ReadFile(id.files.RootsFile)
	if err != nil {
		return
	}
	sum := sha256.Sum256(pemRoots)
	if sum == id.rootsLast {
		return
	}
	id.rootsLast = sum
	if slices.Contains(id.rootsSeen, sum) {
		return
	}
	pool := id.roots.Load().Clone()
	if !pool.AppendCertsFromPEM(pemRoots) {
		id.failures.Add(1)
		return
	}
	id.rootsSeen = append(id.rootsSeen, sum)
	id.roots.Store(pool)
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
	return check(PeerIdentityValue{Leaf: leaf, Chain: chains[0], SPKI: SPKI(leaf), URIs: leaf.URIs, NotAfter: leaf.NotAfter})
}

// base is the configuration both ends share: TLS 1.3 only, no session
// resumption, and the certificate asked of the identity at every handshake
// so a reload is picked up without rebuilding the config.
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

// PeerOf returns the peer of a completed handshake, false before one or
// without a peer certificate. Its Chain is set only when the standard
// verifier ran; the identity's own check sees the chain it verified.
func PeerOf(cs tls.ConnectionState) (PeerIdentityValue, bool) {
	if !cs.HandshakeComplete || len(cs.PeerCertificates) == 0 {
		return PeerIdentityValue{}, false
	}
	leaf := cs.PeerCertificates[0]
	p := PeerIdentityValue{Leaf: leaf, SPKI: SPKI(leaf), URIs: leaf.URIs, NotAfter: leaf.NotAfter}
	if len(cs.VerifiedChains) > 0 {
		// Under VerifyConnection the standard verifier does not run and
		// this stays empty: the presented certificates are never handed
		// back as the verified chain (review of PR #301, L1).
		p.Chain = cs.VerifiedChains[0]
	}
	return p, true
}

// Deadline is when a connection to peer must end: at its certificate's
// NotAfter, or after maxAge from opened, whichever comes first, so a
// revoked or expired identity cannot ride an old connection.
func Deadline(peer PeerIdentityValue, opened time.Time, maxAge time.Duration) time.Time {
	d := opened.Add(maxAge)
	if !peer.NotAfter.IsZero() && peer.NotAfter.Before(d) {
		return peer.NotAfter
	}
	return d
}
