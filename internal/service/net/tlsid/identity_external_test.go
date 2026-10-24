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

// look waits past the identity's Every (the files helper sets a millisecond)
// and asks for the certificate, as every handshake does, so the identity
// looks at its files.
func look(t *testing.T, id *tlsid.Identity) *tls.Certificate {
	t.Helper()
	time.Sleep(10 * time.Millisecond)
	return must(id.Certificate())(t)
}

// writeAt writes b to path and gives it the modification time at, as a copy
// that keeps its source's time (cp -p, rsync -a, tar x) or a filesystem of a
// coarse granularity leaves it.
func writeAt(t *testing.T, path string, b []byte, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// stampAll gives every file of f the one modification time at, so the
// newest of them, the time a look kept, is at.
func stampAll(t *testing.T, f tlsid.FilesConfig, at time.Time) {
	t.Helper()
	for _, p := range []string{f.CertFile, f.KeyFile, f.RootsFile} {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// trusts reports whether the identity's roots verify a client certificate.
func trusts(t *testing.T, id *tlsid.Identity, certPEM []byte) error {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	if b == nil {
		t.Fatal("no PEM block")
	}
	leaf := must(x509.ParseCertificate(b.Bytes))(t)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: id.Roots(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

// A CA rollover's bundle copied with its source's modification time kept
// (cp -p, rsync -a, tar x, a package), older than the certificate's, is read
// at the next look: a look decides by the files' bytes, not their times
// (#633). Before, a look read nothing unless the newest time of the three
// files was after the one it last kept, so the running node refused every
// peer of the new CA until it restarted.
func TestIdentityTrustsARolloverBundleCopiedWithItsTimeKept(t *testing.T) {
	c := newCA(t, "ca")
	f := files(t, c, "kitsunium-consensus://aa/slot/s", nil)
	id := must(tlsid.LoadReloadable(f))(t)
	next := newCA(t, "next")
	bundle := append(slices.Clone(c.pem), next.pem...)
	writeAt(t, f.RootsFile, bundle, time.Now().Add(-24*time.Hour))
	look(t, id)
	// The peer of the new CA trusts both, as the rollover deploys.
	nf := files(t, next, "kitsunium-consensus://aa/slot/n", nil)
	writeAt(t, nf.RootsFile, bundle, time.Now())
	nb := must(tlsid.LoadReloadable(nf))(t)
	if hs := handshake(id, nb, nil, nil); hs.server != nil {
		t.Fatalf("the new CA of a bundle copied with an older time is not trusted: %v", hs.server)
	}
	ob := must(tlsid.LoadReloadable(files(t, c, "kitsunium-consensus://aa/slot/o", nil)))(t)
	if hs := handshake(id, ob, nil, nil); hs.server != nil {
		t.Fatalf("the old CA is no longer trusted during the rollover: %v", hs.server)
	}
	if n := id.ReloadFailures(); n != 0 {
		t.Fatalf("reload failures %d, want 0", n)
	}
}

// A renewal whose files carry the very time a look kept is served at the
// next look (#633): a filesystem of a one-second granularity (ext3, HFS+,
// FAT, many NFS servers) or a copy that keeps times lands it there. Before,
// the roots file rewritten at T made a look keep T, and a renewal landing at
// T was never read: the node kept presenting the old certificate until it
// expired, and every peer refused it from then on, ReloadFailures still 0.
func TestIdentityServesARenewalLandingAtTheTimeALookKept(t *testing.T) {
	c := newCA(t, "ca")
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	f := files(t, c, "kitsunium-consensus://aa/slot/s", key)
	id := must(tlsid.LoadReloadable(f))(t)
	first := must(id.Leaf())(t)
	// The roots file is rewritten at T, later than the pair, and a look
	// keeps T.
	at := time.Now().Add(time.Minute).Truncate(time.Second)
	writeAt(t, f.RootsFile, c.pem, at)
	look(t, id)
	// The renewal, same key and SAN, lands at T.
	cp, kp := c.issue(t, "kitsunium-consensus://aa/slot/s", key)
	writeAt(t, f.CertFile, cp, at)
	writeAt(t, f.KeyFile, kp, at)
	look(t, id)
	if renewed := must(id.Leaf())(t); renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("the renewal on disk is not served: the look kept the certificate it already had")
	}
	if n := id.ReloadFailures(); n != 0 {
		t.Fatalf("reload failures %d, want 0", n)
	}
}

// Whatever time a changed file carries, older than the one the last look
// kept or the same, the next look reads it: the roots file as the pair
// (#633).
func TestIdentityReadsAChangedFileWhateverItsTime(t *testing.T) {
	const san = "kitsunium-consensus://aa/slot/s"
	cases := []struct {
		name  string
		roots bool
		shift time.Duration
	}{
		{name: "roots, older", roots: true, shift: -24 * time.Hour},
		{name: "roots, same", roots: true},
		{name: "pair, older", shift: -24 * time.Hour},
		{name: "pair, same"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCA(t, "ca")
			key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
			f := files(t, c, san, key)
			at := time.Now().Truncate(time.Second)
			stampAll(t, f, at)
			id := must(tlsid.LoadReloadable(f))(t)
			first := must(id.Leaf())(t)
			when := at.Add(tc.shift)
			if tc.roots {
				next := newCA(t, "next")
				writeAt(t, f.RootsFile, append(slices.Clone(c.pem), next.pem...), when)
				look(t, id)
				peer, _ := next.issue(t, "kitsunium-consensus://aa/slot/n", nil)
				if err := trusts(t, id, peer); err != nil {
					t.Fatalf("the new roots are not read: %v", err)
				}
			} else {
				cp, kp := c.issue(t, san, key)
				writeAt(t, f.CertFile, cp, when)
				writeAt(t, f.KeyFile, kp, when)
				look(t, id)
				if renewed := must(id.Leaf())(t); renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
					t.Fatal("the renewal is not read")
				}
			}
			if n := id.ReloadFailures(); n != 0 {
				t.Fatalf("reload failures %d, want 0", n)
			}
		})
	}
}

// A look over files whose bytes did not change swaps nothing and parses
// nothing, even after every time moved forward (a touch, a backup tool, a
// copy over itself): the certificate and the roots stay the very same values,
// and nothing is counted. Within Every the files are not looked at at all.
func TestIdentityLookWithoutAChangeReloadsNothing(t *testing.T) {
	c := newCA(t, "ca")
	f := files(t, c, "kitsunium-consensus://aa/slot/s", nil)
	id := must(tlsid.LoadReloadable(f))(t)
	cert, roots := must(id.Certificate())(t), id.Roots()
	if got := look(t, id); got != cert || id.Roots() != roots {
		t.Fatal("a look over unchanged files reloaded them")
	}
	stampAll(t, f, time.Now().Add(time.Hour))
	if got := look(t, id); got != cert || id.Roots() != roots {
		t.Fatal("a look reloaded files whose times moved but bytes did not")
	}
	for _, p := range []string{f.CertFile, f.KeyFile, f.RootsFile} {
		writeAt(t, p, must(os.ReadFile(p))(t), time.Now().Add(2*time.Hour))
	}
	if got := look(t, id); got != cert || id.Roots() != roots {
		t.Fatal("a look reloaded files written again with the same bytes")
	}
	if n := id.ReloadFailures(); n != 0 {
		t.Fatalf("reload failures %d, want 0", n)
	}

	// Within Every, a renewal is not even looked at.
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	slow := files(t, c, "kitsunium-consensus://aa/slot/s", key)
	slow.Every = time.Hour
	sid := must(tlsid.LoadReloadable(slow))(t)
	held := must(sid.Certificate())(t)
	cp, kp := c.issue(t, "kitsunium-consensus://aa/slot/s", key)
	writeAt(t, slow.CertFile, cp, time.Now().Add(time.Hour))
	writeAt(t, slow.KeyFile, kp, time.Now().Add(time.Hour))
	if got := look(t, sid); got != held {
		t.Fatal("the files were looked at before Every passed")
	}
}

// A pair caught mid-write, its certificate cut short as a writer that
// truncates then writes leaves it, is refused, counted once however many
// looks see it, and the old certificate is served meanwhile; once whole, it
// is taken at the next look, whatever its time (review of PR #301, L3;
// #633).
func TestIdentityRefusesATornPairOnceAndTakesItWhole(t *testing.T) {
	const san = "kitsunium-consensus://aa/slot/s"
	c := newCA(t, "ca")
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	f := files(t, c, san, key)
	at := time.Now().Truncate(time.Second)
	stampAll(t, f, at)
	id := must(tlsid.LoadReloadable(f))(t)
	first := must(id.Certificate())(t)
	cp, _ := c.issue(t, san, key)
	writeAt(t, f.CertFile, cp[:len(cp)/2], at)
	for range 3 {
		if got := look(t, id); got != first {
			t.Fatal("a torn certificate replaced the one served")
		}
	}
	if n := id.ReloadFailures(); n != 1 {
		t.Fatalf("reload failures %d, want the torn pair counted once", n)
	}
	writeAt(t, f.CertFile, cp, at)
	look(t, id)
	if renewed := must(id.Leaf())(t); renewed.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Fatal("the pair, once whole, is not taken")
	}
	if n := id.ReloadFailures(); n != 1 {
		t.Fatalf("reload failures %d, want 1", n)
	}
}

// A roots file that names no certificate, emptied or garbage, is counted
// once however many looks see it, and never replaces the roots; one that
// cannot be read is no change and is not counted. Throughout, the roots
// still trust their CA, and a valid bundle afterwards is added to them
// (ADR 0174 D1, review of PR #301, L2).
func TestIdentityNeverNarrowsTheRootsOnAnUnusableRootsFile(t *testing.T) {
	c := newCA(t, "ca")
	f := files(t, c, "kitsunium-consensus://aa/slot/s", nil)
	id := must(tlsid.LoadReloadable(f))(t)
	peer, _ := c.issue(t, "kitsunium-consensus://aa/slot/p", nil)
	steps := []struct {
		name     string
		write    func()
		failures int64
	}{
		{name: "emptied", write: func() { writeAt(t, f.RootsFile, nil, time.Now().Add(time.Hour)) }, failures: 1},
		{name: "garbage", write: func() { writeAt(t, f.RootsFile, []byte("not a PEM"), time.Now().Add(2*time.Hour)) }, failures: 2},
		{name: "removed", write: func() {
			if err := os.Remove(f.RootsFile); err != nil {
				t.Fatal(err)
			}
		}, failures: 2},
	}
	for _, s := range steps {
		s.write()
		for range 3 {
			look(t, id)
		}
		if n := id.ReloadFailures(); n != s.failures {
			t.Fatalf("%s: reload failures %d, want %d", s.name, n, s.failures)
		}
		if err := trusts(t, id, peer); err != nil {
			t.Fatalf("%s: the roots no longer trust their CA: %v", s.name, err)
		}
	}
	next := newCA(t, "next")
	writeAt(t, f.RootsFile, append(slices.Clone(c.pem), next.pem...), time.Now().Add(-time.Hour))
	look(t, id)
	np, _ := next.issue(t, "kitsunium-consensus://aa/slot/n", nil)
	if err := trusts(t, id, np); err != nil {
		t.Fatalf("a valid bundle after an unusable one is not added: %v", err)
	}
	if err := trusts(t, id, peer); err != nil {
		t.Fatalf("the old CA is no longer trusted: %v", err)
	}
}
