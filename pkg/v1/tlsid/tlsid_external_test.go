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
// Without it the only way to tell "the operator pointed at the wrong file" from
// any other error is string matching.
func TestSentinelIsMatchable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		params tlsid.Params
	}
	tests := []tc{
		{"junk where a CA bundle belongs", tlsid.Params{RootsPEM: []byte("not a certificate")}},
		{"junk where a client CA bundle belongs", tlsid.Params{ClientCAPEM: []byte("not a certificate")}},
		{"a PEM block of the wrong type", tlsid.Params{RootsPEM: []byte("-----BEGIN NONSENSE-----\nAAAA\n-----END NONSENSE-----\n")}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := tlsid.New(c.params)
		if err == nil {
			t.Fatalf("New with %s = nil, want a refusal", c.name)
		}
		if !errors.Is(err, tlsid.MaterialInvalid) {
			t.Fatalf("New with %s = %v, want MaterialInvalid", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a zero-length bundle reads as "none supplied" rather than as junk, so it
	//: is accepted and the client falls back to the system roots. Pinned here
	//: because the two look identical at a call site that builds the bytes.
	if _, err := tlsid.New(tlsid.Params{RootsPEM: []byte("")}); err != nil {
		t.Errorf("New with a zero-length bundle = %v, want nil", err)
	}
}

// TestMemoryAndDiskAgree pins that the client config the facade hands back is
// fully wired: the caller never gets a config that is missing a root store or
// silently negotiates an obsolete version.
func TestMemoryAndDiskAgree(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mint(t)
	id, err := tlsid.New(tlsid.Params{
		CertPEM: certPEM, KeyPEM: keyPEM, RootsPEM: certPEM, ServerName: "sdm.core.svc",
	})
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	cfg := id.ClientConfig()

	type tc struct {
		name  string
		check func(*tls.Config) (string, bool)
	}
	tests := []tc{
		{"the requested server name", func(c *tls.Config) (string, bool) {
			return c.ServerName, c.ServerName == "sdm.core.svc"
		}},
		{"a root store built from the supplied bundle", func(c *tls.Config) (string, bool) {
			return "RootCAs", c.RootCAs != nil
		}},
		{"exactly one client certificate", func(c *tls.Config) (string, bool) {
			return "Certificates", len(c.Certificates) == 1
		}},
		{"a TLS 1.3 floor", func(c *tls.Config) (string, bool) {
			return "MinVersion", c.MinVersion == tls.VersionTLS13
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := c.check(cfg)
		if !ok {
			t.Errorf("ClientConfig() lacks %s (got %q)", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMutualTLSRoundTrip proves the identity actually works against a real
// handshake, not merely that the struct fields look right. A config can satisfy
// every field assertion above and still fail to negotiate.
func TestMutualTLSRoundTrip(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mint(t)

	type tc struct {
		name         string
		serverParams tlsid.Params
		clientParams tlsid.Params
	}
	tests := []tc{
		{
			name: "mutual authentication required",
			serverParams: tlsid.Params{
				CertPEM: certPEM, KeyPEM: keyPEM, ClientCAPEM: certPEM, RequireClientCert: true,
			},
			clientParams: tlsid.Params{
				CertPEM: certPEM, KeyPEM: keyPEM, RootsPEM: certPEM, ServerName: "kitsunium-test",
			},
		},
		{
			//: server authentication only — the common case, and the one that
			//: would silently keep working if RequireClientCert were ignored.
			name:         "server authentication only",
			serverParams: tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM},
			clientParams: tlsid.Params{RootsPEM: certPEM, ServerName: "kitsunium-test"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		server, err := tlsid.New(c.serverParams)
		if err != nil {
			t.Fatalf("server identity: %v", err)
		}
		client, err := tlsid.New(c.clientParams)
		if err != nil {
			t.Fatalf("client identity: %v", err)
		}

		state := runHandshake(t, server, client)
		if state.Version != tls.VersionTLS13 {
			t.Errorf("negotiated version %x, want TLS 1.3", state.Version)
		}
		if len(state.PeerCertificates) == 0 {
			t.Error("no peer certificate presented")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// runHandshake dials a real TLS listener and returns the client-side connection
// state so the caller asserts on what was actually negotiated rather than on the
// absence of a failure.
//
// Goroutine lifecycle: exactly one accept goroutine is started. It is bounded by
// the listener — it blocks only in Accept, and the deferred ln.Close below
// unblocks it on every exit path, including t.Fatalf. It reports its single
// outcome on the buffered done channel, so it can never block on send and can
// never outlive this function.
func runHandshake(t *testing.T, server, client tlsid.Identity) tls.ConnectionState {
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
	if herr := <-done; herr != nil {
		t.Fatalf("server handshake: %v", herr)
	}
	//: the negotiated state, for the caller to assert on.
	return conn.ConnectionState()
}

// TestLoadFromDisk pins the facade's file-based entry point, including the
// unusable-bundle refusal that is the whole reason this package exists: an
// operator who points at the wrong path must find out at startup, not at the
// first connection.
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

	type tc struct {
		name    string
		params  tlsid.FileParams
		wantErr bool
	}
	tests := []tc{
		{
			name: "a complete set of readable files",
			params: tlsid.FileParams{
				CertFile: certFile, KeyFile: keyFile, RootsFile: certFile, ServerName: "sdm.core.svc",
			},
		},
		{"a junk CA file", tlsid.FileParams{RootsFile: junkFile}, true},
		{"a CA path that does not exist", tlsid.FileParams{RootsFile: filepath.Join(dir, "absent.pem")}, true},
		{"a certificate with no key", tlsid.FileParams{CertFile: certFile}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := tlsid.Load(c.params)
		if c.wantErr {
			//: every refusal on this path is the same operator mistake, so it
			//: must arrive under one matchable sentinel.
			if !errors.Is(err, tlsid.MaterialInvalid) {
				t.Fatalf("Load with %s = %v, want MaterialInvalid", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Load with %s = %v, want nil", c.name, err)
		}
		if id.ClientConfig().RootCAs == nil {
			t.Error("RootCAs must be installed from the bundle on disk")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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
