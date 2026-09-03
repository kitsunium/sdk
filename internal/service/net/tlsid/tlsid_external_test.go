package tlsid_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/tlsid"
)

// materialDir holds the paths of a freshly minted certificate on disk.
type materialDir struct {
	Cert  string
	Key   string
	Junk  string
	Empty string
}

// writeMaterial mints a self-signed certificate and writes the fixtures a test
// needs, so nothing depends on checked-in files that could expire.
func writeMaterial(t *testing.T) materialDir {
	t.Helper()
	dir := t.TempDir()
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
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	md := materialDir{
		Cert:  filepath.Join(dir, "cert.pem"),
		Key:   filepath.Join(dir, "key.pem"),
		Junk:  filepath.Join(dir, "junk.pem"),
		Empty: filepath.Join(dir, "empty.pem"),
	}
	write(t, md.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write(t, md.Key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	write(t, md.Junk, []byte("this is a text file, not a certificate\n"))
	write(t, md.Empty, nil)
	return md
}

// write puts one fixture on disk.
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// loadCase describes one Load scenario.
type loadCase struct {
	name    string
	params  func(md materialDir) corenet.IdentityFileParams
	wantErr bool
}

// TestLoadRefusesUnusableMaterial pins that every way of getting TLS material
// wrong on disk is a loud typed failure rather than a silent downgrade. The
// junk-file case is the one that matters most in practice: an operator points
// the CA path at the wrong file and, without this check, gets a client that
// trusts nothing while reporting nothing useful.
func TestLoadRefusesUnusableMaterial(t *testing.T) {
	t.Parallel()
	cases := []loadCase{
		{
			name:   "no material at all is a verify-only identity",
			params: func(materialDir) corenet.IdentityFileParams { return corenet.IdentityFileParams{} },
		},
		{
			name: "a real CA bundle loads",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{RootsFile: md.Cert}
			},
		},
		{
			name: "a text file as CA bundle",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{RootsFile: md.Junk}
			},
			wantErr: true,
		},
		{
			name: "an empty CA bundle must not widen trust silently",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{RootsFile: md.Empty}
			},
			wantErr: true,
		},
		{
			name: "a missing CA bundle",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{RootsFile: md.Cert + ".absent"}
			},
			wantErr: true,
		},
		{
			name: "certificate without key must not degrade to plain TLS",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{CertFile: md.Cert}
			},
			wantErr: true,
		},
		{
			name: "a full client keypair loads",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{CertFile: md.Cert, KeyFile: md.Key}
			},
		},
		{
			name: "mutual TLS without a client CA bundle",
			params: func(md materialDir) corenet.IdentityFileParams {
				return corenet.IdentityFileParams{CertFile: md.Cert, KeyFile: md.Key, RequireClientCert: true}
			},
			wantErr: true,
		},
	}
	md := writeMaterial(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runLoadCase(t, tc, md)
		})
	}
}

// runLoadCase executes one loadCase against Load.
func runLoadCase(t *testing.T, tc loadCase, md materialDir) {
	t.Helper()
	_, err := tlsid.Load(tc.params(md))
	if tc.wantErr {
		if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
			t.Fatalf("expected TLS_MATERIAL_INVALID, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLoadCarriesServerName pins the field that a port-forwarded or tunnelled
// deployment cannot work without: the dial address is 127.0.0.1 while the peer
// certificate names the real service, so the handshake fails until ServerName
// overrides the verified name.
func TestLoadCarriesServerName(t *testing.T) {
	t.Parallel()
	md := writeMaterial(t)
	id, err := tlsid.Load(corenet.IdentityFileParams{RootsFile: md.Cert, ServerName: "sdm.core.svc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := id.ClientConfig().ServerName; got != "sdm.core.svc" {
		t.Fatalf("ServerName = %q, want \"sdm.core.svc\"", got)
	}
}

// TestLoadedIdentityStaysRedacted pins that going through the filesystem does
// not create a second, unredacted path to the material.
func TestLoadedIdentityStaysRedacted(t *testing.T) {
	t.Parallel()
	md := writeMaterial(t)
	id, err := tlsid.Load(corenet.IdentityFileParams{CertFile: md.Cert, KeyFile: md.Key})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.String() != "<redacted>" || id.GoString() != "<redacted>" {
		t.Fatalf("identity is not redacted: %s / %#v", id.String(), id)
	}
	if id.IsZero() {
		t.Fatal("a loaded identity must not report IsZero")
	}
	if got := id.ClientConfig().MinVersion; got != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", got)
	}
}
