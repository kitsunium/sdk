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
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/tlsid"
)

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T, name string) *ca {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &ca{cert: c, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (c *ca) issue(t *testing.T, san string, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte) {
	t.Helper()
	if key == nil {
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	u, _ := url.Parse(san)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{u}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
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

func files(t *testing.T, c *ca, san string, key *ecdsa.PrivateKey) tlsid.Files {
	dir := t.TempDir()
	cp, kp := c.issue(t, san, key)
	return tlsid.Files{
		CertFile: writeFile(t, dir, "cert.pem", cp), KeyFile: writeFile(t, dir, "key.pem", kp),
		RootsFile: writeFile(t, dir, "ca.pem", c.pem), Every: time.Millisecond,
	}
}

// handshake runs one mutual handshake over a pipe and returns both errors
// and what each side's check saw.
func handshake(server, client *tlsid.Identity, sc, cc tlsid.Verify) (error, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err, err
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
		_ = a.SetDeadline(time.Now().Add(5 * time.Second))
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
		return err, err
	}
	defer b.Close()
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))
	c := tls.Client(b, client.ClientConfig(cc))
	cerr := c.Handshake()
	if cerr == nil {
		var one [1]byte
		_, cerr = c.Read(one[:])
	}
	return <-done, cerr
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
	var seen tlsid.PeerIdentity
	serr, cerr := handshake(srv, cli, func(p tlsid.PeerIdentity) error { seen = p; return nil }, nil)
	if serr != nil || cerr != nil {
		t.Fatalf("handshake: %v / %v", serr, cerr)
	}
	leaf, _ := cli.Leaf()
	if seen.SPKI != tlsid.SPKI(leaf) || len(seen.URIs) != 1 || seen.URIs[0].String() != "kitsunium-consensus://aa/slot/c" {
		t.Fatalf("peer identity %+v", seen)
	}
	// The check is fail-closed, on either side.
	refuse := errors.New("no")
	if serr, _ := handshake(srv, cli, func(tlsid.PeerIdentity) error { return refuse }, nil); serr == nil {
		t.Fatal("the server's check did not fail the handshake")
	}
	if _, cerr := handshake(srv, cli, nil, func(tlsid.PeerIdentity) error { return refuse }); cerr == nil {
		t.Fatal("the client's check did not fail the handshake")
	}
	// Another CA's certificate fails the chain before any check runs.
	other := newCA(t, "other")
	stranger, _ := tlsid.LoadReloadable(files(t, other, "kitsunium-consensus://aa/slot/x", nil))
	called := false
	if serr, _ := handshake(srv, stranger, func(tlsid.PeerIdentity) error { called = true; return nil }, nil); serr == nil || called {
		t.Fatalf("another CA was accepted (check called %v)", called)
	}
	if !srv.ServerConfig(nil).SessionTicketsDisabled || !srv.ClientConfig(nil).SessionTicketsDisabled {
		t.Fatal("session tickets enabled")
	}
}

func TestIdentityReloadsInPlace(t *testing.T) {
	c := newCA(t, "ca")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := files(t, c, "kitsunium-consensus://aa/slot/s", key)
	id, err := tlsid.LoadReloadable(f)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := id.Leaf()
	bump := func(certPEM, keyPEM []byte) {
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(f.CertFile, certPEM, 0o600)
		_ = os.WriteFile(f.KeyFile, keyPEM, 0o600)
		later := time.Now().Add(time.Second)
		_ = os.Chtimes(f.CertFile, later, later)
	}
	// A renewal over the same key and SAN is taken.
	cp, kp := c.issue(t, "kitsunium-consensus://aa/slot/s", key)
	bump(cp, kp)
	renewed, _ := id.Leaf()
	if renewed.SerialNumber.Cmp(first.SerialNumber) == 0 || id.ReloadFailures() != 0 {
		t.Fatalf("renewal not taken (failures %d)", id.ReloadFailures())
	}
	// Another key, or another SAN, is refused and counted; the old stays.
	cp, kp = c.issue(t, "kitsunium-consensus://aa/slot/s", nil)
	bump(cp, kp)
	kept, _ := id.Leaf()
	if kept.SerialNumber.Cmp(renewed.SerialNumber) != 0 || id.ReloadFailures() != 1 {
		t.Fatalf("a new key was accepted (failures %d)", id.ReloadFailures())
	}
	cp, kp = c.issue(t, "kitsunium-consensus://aa/slot/other", key)
	bump(cp, kp)
	if again, _ := id.Leaf(); again.SerialNumber.Cmp(renewed.SerialNumber) != 0 || id.ReloadFailures() != 2 {
		t.Fatalf("a new SAN was accepted (failures %d)", id.ReloadFailures())
	}
	// A CA rollover: the new roots are added, the old ones kept.
	next := newCA(t, "next")
	_ = os.WriteFile(f.RootsFile, next.pem, 0o600)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(f.RootsFile, later, later)
	time.Sleep(20 * time.Millisecond)
	_, _ = id.Certificate()
	// The peer issued by the new CA trusts both, as a rollover deploys.
	nf := files(t, next, "kitsunium-consensus://aa/slot/n", nil)
	_ = os.WriteFile(nf.RootsFile, append(append([]byte(nil), c.pem...), next.pem...), 0o600)
	nb, _ := tlsid.LoadReloadable(nf)
	ob, _ := tlsid.LoadReloadable(files(t, c, "kitsunium-consensus://aa/slot/o", nil))
	if serr, _ := handshake(id, nb, nil, nil); serr != nil {
		t.Fatalf("the new CA is not trusted: %v", serr)
	}
	if serr, _ := handshake(id, ob, nil, nil); serr != nil {
		t.Fatalf("the old CA is no longer trusted during the rollover: %v", serr)
	}
}
