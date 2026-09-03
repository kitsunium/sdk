package tlsid_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// mint returns PEM certificate and key bytes for a throwaway identity.
func mint(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		// SANs, not CommonName: Go refuses to verify a certificate that relies on
		// the legacy field, so a CommonName-only fixture fails the handshake.
		DNSNames:    []string{"kitsunium-test"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// TestSentinelIsMatchable pins that a consumer can branch on the failure without
// reaching into internal packages — the facade must export a usable sentinel.
func TestSentinelIsMatchable(t *testing.T) {
	t.Parallel()
	_, err := tlsid.New(tlsid.Params{RootsPEM: []byte("not a certificate")})
	if err == nil {
		t.Fatal("expected an unusable bundle to be refused")
	}
	if !errors.Is(err, tlsid.MaterialInvalid) {
		t.Fatalf("errors.Is(err, MaterialInvalid) = false, got %v", err)
	}
}

// TestMemoryAndDiskAgree pins the contract that both entry points apply the same
// rules — a difference would let one path accept what the other refuses.
func TestMemoryAndDiskAgree(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mint(t)
	id, err := tlsid.New(tlsid.Params{
		CertPEM: certPEM, KeyPEM: keyPEM, RootsPEM: certPEM, ServerName: "sdm.core.svc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := id.ClientConfig()
	if cfg.ServerName != "sdm.core.svc" {
		t.Fatalf("ServerName = %q", cfg.ServerName)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs must be installed when a bundle was supplied")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want 1", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
}

// TestMutualTLSRoundTrip proves the identity actually works against a real
// handshake, not merely that the struct fields look right.
func TestMutualTLSRoundTrip(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mint(t)
	server, err := tlsid.New(tlsid.Params{
		CertPEM: certPEM, KeyPEM: keyPEM, ClientCAPEM: certPEM, RequireClientCert: true,
	})
	if err != nil {
		t.Fatalf("server identity: %v", err)
	}
	client, err := tlsid.New(tlsid.Params{
		CertPEM: certPEM, KeyPEM: keyPEM, RootsPEM: certPEM, ServerName: "kitsunium-test",
	})
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}
	runHandshake(t, server, client)
}

// runHandshake dials a real TLS listener and asserts mutual authentication.
//
// Goroutine lifecycle: exactly one accept goroutine is started. It is bounded by
// the listener — it blocks only in Accept, and the deferred ln.Close below
// unblocks it on every exit path, including t.Fatalf. It reports its single
// outcome on the buffered done channel, so it can never block on send and can
// never outlive this function.
func runHandshake(t *testing.T, server, client tlsid.Identity) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", server.ServerConfig())
	//: without a listener there is nothing to hand-shake against.
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer closeOrFail(t, ln)
	done := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		//: a closed listener ends the goroutine through this path.
		if aerr != nil {
			done <- aerr
			return
		}
		defer closeOrFail(t, conn)
		done <- conn.(*tls.Conn).Handshake()
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), client.ClientConfig())
	//: a dial failure is the assertion this test most often trips.
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, conn)
	//: the server side must also have completed the handshake.
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	state := conn.ConnectionState()
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated version %x, want TLS 1.3", state.Version)
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificate presented")
	}
}

// TestLoadFromDisk pins the facade's file-based entry point, including the
// unusable-bundle refusal that is the whole reason this package exists.
func TestLoadFromDisk(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mint(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	junkFile := filepath.Join(dir, "junk.pem")
	for path, data := range map[string][]byte{
		certFile: certPEM,
		keyFile:  keyPEM,
		junkFile: []byte("definitely not a certificate\n"),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	id, err := tlsid.Load(tlsid.FileParams{
		CertFile: certFile, KeyFile: keyFile, RootsFile: certFile, ServerName: "sdm.core.svc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.ClientConfig().RootCAs == nil {
		t.Fatal("RootCAs must be installed from the bundle on disk")
	}
	//: the operator-points-at-the-wrong-file case must fail loudly.
	if _, err := tlsid.Load(tlsid.FileParams{RootsFile: junkFile}); !errors.Is(err, tlsid.MaterialInvalid) {
		t.Fatalf("expected MaterialInvalid for a junk CA file, got %v", err)
	}
}

// closeOrFail closes c and reports a close error rather than discarding it — a
// silently dropped Close can mask a socket leak that only shows up under load.
func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	//: a close failure is reported, never swallowed.
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
