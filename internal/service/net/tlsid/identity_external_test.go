package tlsid_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/tlsid"
)

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

// must curries a fallible setup step: must(step())(t) yields the step's
// value, or stops the test when the step failed. Every value it guards is one
// the test goes on to rely on, so continuing would assert on garbage. The
// curried shape exists because Go spreads a multi-value call only when it is
// the sole argument.
func must[T any](v T, err error) func(testing.TB) T {
	return func(t testing.TB) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func newCA(t *testing.T, name string) *ca {
	t.Helper()
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c := must(x509.ParseCertificate(der))(t)
	return &ca{cert: c, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (c *ca) issue(t *testing.T, san string, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte) {
	t.Helper()
	if key == nil {
		key = must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	}
	u := must(url.Parse(san))(t)
	serial := must(rand.Int(rand.Reader, big.NewInt(1<<62)))(t)
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{u}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	kb := must(x509.MarshalECPrivateKey(key))(t)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func files(t *testing.T, c *ca, san string, key *ecdsa.PrivateKey) tlsid.FilesConfig {
	dir := t.TempDir()
	cp, kp := c.issue(t, san, key)
	return tlsid.FilesConfig{
		CertFile: writeFile(t, dir, "cert.pem", cp), KeyFile: writeFile(t, dir, "key.pem", kp),
		RootsFile: writeFile(t, dir, "ca.pem", c.pem), Every: time.Millisecond,
	}
}

// handshakeResult is what each side of one mutual handshake reported: the
// two outcomes are independent, since a refusal by one side's check is seen
// by the other only as a broken connection.
type handshakeResult struct {
	server error
	client error
}

// handshake runs one mutual handshake over loopback TCP and returns what each
// side reported. The server side runs in its own goroutine; it ends after one
// handshake (or one failed Accept), and its 5 s deadline bounds it even when
// the client side gave up early, so no goroutine outlives the call for long.
func handshake(server, client *tlsid.Identity, sc, cc tlsid.Verify) handshakeResult {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return handshakeResult{server: err, client: err}
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		a, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer a.Close()
		if err := a.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			done <- err
			return
		}
		s := tls.Server(a, server.ServerConfig(sc))
		err = s.Handshake()
		if err == nil {
			// A TLS 1.3 client learns of a refused client certificate only
			// on its first read: give it one byte.
			_, err = s.Write([]byte{1})
		}
		done <- err
	}()
	b, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return handshakeResult{server: err, client: err}
	}
	defer b.Close()
	if err := b.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return handshakeResult{server: err, client: err}
	}
	c := tls.Client(b, client.ClientConfig(cc))
	cerr := c.Handshake()
	if cerr == nil {
		var one [1]byte
		_, cerr = c.Read(one[:])
	}
	return handshakeResult{server: <-done, client: cerr}
}

func TestIdentityVerifiesEveryHandshakeAndExposesThePeer(t *testing.T) {
	c := newCA(t, "ca")
	srv, err := tlsid.LoadReloadable(files(t, c, "kitsunium-consensus://aa/slot/s", nil))
	if err != nil {
		t.Fatal(err)
	}
	cli, err := tlsid.LoadReloadable(files(t, c, "kitsunium-consensus://aa/slot/c", nil))
	if err != nil {
		t.Fatal(err)
	}
	var seen tlsid.PeerIdentityValue
	hs := handshake(srv, cli, func(p tlsid.PeerIdentityValue) error { seen = p; return nil }, nil)
	if hs.server != nil || hs.client != nil {
		t.Fatalf("handshake: %v / %v", hs.server, hs.client)
	}
	leaf := must(cli.Leaf())(t)
	if seen.SPKI != tlsid.SPKI(leaf) || len(seen.URIs) != 1 || seen.URIs[0].String() != "kitsunium-consensus://aa/slot/c" {
		t.Fatalf("peer identity %+v", seen)
	}
	// The check is fail-closed, on either side.
	refuse := errors.New("no")
	if hs := handshake(srv, cli, func(tlsid.PeerIdentityValue) error { return refuse }, nil); hs.server == nil {
		t.Fatal("the server's check did not fail the handshake")
	}
	if hs := handshake(srv, cli, nil, func(tlsid.PeerIdentityValue) error { return refuse }); hs.client == nil {
		t.Fatal("the client's check did not fail the handshake")
	}
	// Another CA's certificate fails the chain before any check runs.
	other := newCA(t, "other")
	stranger := must(tlsid.LoadReloadable(files(t, other, "kitsunium-consensus://aa/slot/x", nil)))(t)
	called := false
	if hs := handshake(srv, stranger, func(tlsid.PeerIdentityValue) error { called = true; return nil }, nil); hs.server == nil || called {
		t.Fatalf("another CA was accepted (check called %v)", called)
	}
	if !srv.ServerConfig(nil).SessionTicketsDisabled || !srv.ClientConfig(nil).SessionTicketsDisabled {
		t.Fatal("session tickets enabled")
	}
}

func TestIdentityReloadsInPlace(t *testing.T) {
	c := newCA(t, "ca")
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	f := files(t, c, "kitsunium-consensus://aa/slot/s", key)
	id, err := tlsid.LoadReloadable(f)
	if err != nil {
		t.Fatal(err)
	}
	first := must(id.Leaf())(t)
	bump := func(certPEM, keyPEM []byte) {
		time.Sleep(20 * time.Millisecond)
		if err := os.WriteFile(f.CertFile, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.KeyFile, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Second)
		if err := os.Chtimes(f.CertFile, later, later); err != nil {
			t.Fatal(err)
		}
	}
	// A renewal over the same key and SAN is taken.
	cp, kp := c.issue(t, "kitsunium-consensus://aa/slot/s", key)
	bump(cp, kp)
	renewed := must(id.Leaf())(t)
	if renewed.SerialNumber.Cmp(first.SerialNumber) == 0 || id.ReloadFailures() != 0 {
		t.Fatalf("renewal not taken (failures %d)", id.ReloadFailures())
	}
	// Another key, or another SAN, is refused and counted; the old stays.
	cp, kp = c.issue(t, "kitsunium-consensus://aa/slot/s", nil)
	bump(cp, kp)
	kept := must(id.Leaf())(t)
	if kept.SerialNumber.Cmp(renewed.SerialNumber) != 0 || id.ReloadFailures() != 1 {
		t.Fatalf("a new key was accepted (failures %d)", id.ReloadFailures())
	}
	cp, kp = c.issue(t, "kitsunium-consensus://aa/slot/other", key)
	bump(cp, kp)
	if again := must(id.Leaf())(t); again.SerialNumber.Cmp(renewed.SerialNumber) != 0 || id.ReloadFailures() != 2 {
		t.Fatalf("a new SAN was accepted (failures %d)", id.ReloadFailures())
	}
	// A CA rollover: the new roots are added, the old ones kept.
	next := newCA(t, "next")
	if err := os.WriteFile(f.RootsFile, next.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f.RootsFile, later, later); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	// The call is made for its side effect, the reload of the roots; it must
	// still serve a certificate while doing so.
	if _, err := id.Certificate(); err != nil {
		t.Fatal(err)
	}
	// The peer issued by the new CA trusts both, as a rollover deploys.
	nf := files(t, next, "kitsunium-consensus://aa/slot/n", nil)
	if err := os.WriteFile(nf.RootsFile, append(slices.Clone(c.pem), next.pem...), 0o600); err != nil {
		t.Fatal(err)
	}
	nb := must(tlsid.LoadReloadable(nf))(t)
	ob := must(tlsid.LoadReloadable(files(t, c, "kitsunium-consensus://aa/slot/o", nil)))(t)
	if hs := handshake(id, nb, nil, nil); hs.server != nil {
		t.Fatalf("the new CA is not trusted: %v", hs.server)
	}
	if hs := handshake(id, ob, nil, nil); hs.server != nil {
		t.Fatalf("the old CA is no longer trusted during the rollover: %v", hs.server)
	}
}

// PeerOf never hands back an unverified chain as the verified one: under
// VerifyConnection the standard verifier did not run, so VerifiedChains is
// empty and Chain stays empty (review of PR #301, L1).
func TestPeerOfNeverReturnsAnUnverifiedChain(t *testing.T) {
	c := newCA(t, "ca")
	cp, _ := c.issue(t, "kitsunium-consensus://aa/slot/s", nil)
	b, _ := pem.Decode(cp)
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := tlsid.PeerOf(tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{leaf}})
	if !ok || p.Leaf != leaf {
		t.Fatalf("peer: %+v %v", p, ok)
	}
	if len(p.Chain) != 0 {
		t.Fatal("PeerOf returned the presented certificates as the verified chain")
	}
}

// A renewal whose key lands after the certificate, within the same mtime
// granularity as the refused first look, is still taken (review of PR #301,
// L3).
func TestIdentityRereadsARefusedRenewal(t *testing.T) {
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	c := newCA(t, "ca")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	f := files(t, c, "kitsunium-consensus://aa/slot/s", key)
	id, err := tlsid.LoadReloadable(f)
	must(err)
	first, err := id.Leaf()
	must(err)
	cp, kp := c.issue(t, "kitsunium-consensus://aa/slot/s", key)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	ob, err := x509.MarshalECPrivateKey(other)
	must(err)
	stamp := time.Now().Add(time.Second).Truncate(time.Second)
	time.Sleep(20 * time.Millisecond)
	// The certificate first, with a key file that does not match it yet.
	must(os.WriteFile(f.CertFile, cp, 0o600))
	must(os.WriteFile(f.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ob}), 0o600))
	must(os.Chtimes(f.CertFile, stamp, stamp))
	must(os.Chtimes(f.KeyFile, stamp, stamp))
	_, err = id.Certificate()
	must(err)
	// Then the key, within the same mtime.
	time.Sleep(20 * time.Millisecond)
	must(os.WriteFile(f.KeyFile, kp, 0o600))
	must(os.Chtimes(f.KeyFile, stamp, stamp))
	time.Sleep(20 * time.Millisecond)
	renewed, err := id.Leaf()
	must(err)
	if renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("the renewal was never read again after a refused first look")
	}
}
