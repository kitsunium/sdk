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
	"strings"
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

// TestLoad pins that every way of getting TLS material wrong on disk is a loud
// typed failure rather than a silent downgrade.
//
// The junk-file case is the one that matters most in practice: an operator
// points the CA path at the wrong file and, without this check, gets a client
// that trusts nothing while reporting nothing useful. The empty-file case is
// the same failure one step earlier — an empty bundle would reach core as "not
// configured" and silently widen trust to the platform store.
func TestLoad(t *testing.T) {
	t.Parallel()
	md := writeMaterial(t)

	type tc struct {
		name   string
		params corenet.IdentityFileParams
		//: the field the refusal must name, so an operator knows WHICH path to
		//: fix rather than only that one of four was wrong.
		wantField string
		wantErr   bool
	}
	tests := []tc{
		{name: "no material at all is a verify-only identity"},
		{
			name:   "a real CA bundle",
			params: corenet.IdentityFileParams{RootsFile: md.Cert},
		},
		{
			name:   "a full client keypair",
			params: corenet.IdentityFileParams{CertFile: md.Cert, KeyFile: md.Key},
		},
		{
			name:    "a text file where a CA bundle belongs",
			params:  corenet.IdentityFileParams{RootsFile: md.Junk},
			wantErr: true,
		},
		{
			name:      "an empty CA bundle",
			params:    corenet.IdentityFileParams{RootsFile: md.Empty},
			wantField: "roots_file",
			wantErr:   true,
		},
		{
			name:      "a CA path that does not exist",
			params:    corenet.IdentityFileParams{RootsFile: md.Cert + ".absent"},
			wantField: "roots_file",
			wantErr:   true,
		},
		{
			name:      "an empty certificate file",
			params:    corenet.IdentityFileParams{CertFile: md.Empty, KeyFile: md.Key},
			wantField: "cert_file",
			wantErr:   true,
		},
		{
			name:      "a key path that does not exist",
			params:    corenet.IdentityFileParams{CertFile: md.Cert, KeyFile: md.Key + ".absent"},
			wantField: "key_file",
			wantErr:   true,
		},
		{
			name:      "an empty client CA file",
			params:    corenet.IdentityFileParams{ClientCAFile: md.Empty},
			wantField: "client_ca_file",
			wantErr:   true,
		},
		{
			//: a certificate with no key must not degrade to plain TLS; the
			//: refusal comes from core, so it names no file field.
			name:    "a certificate with no key",
			params:  corenet.IdentityFileParams{CertFile: md.Cert},
			wantErr: true,
		},
		{
			name: "mutual TLS with no client CA bundle",
			params: corenet.IdentityFileParams{
				CertFile: md.Cert, KeyFile: md.Key, RequireClientCert: true,
			},
			wantErr: true,
		},
		{
			//: the field a port-forwarded or tunnelled deployment cannot work
			//: without: the dial address is 127.0.0.1 while the peer
			//: certificate names the real service.
			name:   "a bundle with an overridden server name",
			params: corenet.IdentityFileParams{RootsFile: md.Cert, ServerName: "sdm.core.svc"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := tlsid.Load(c.params)
		if c.wantErr {
			//: every refusal on this path is the same operator mistake, so it
			//: must arrive under one matchable sentinel.
			if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
				t.Fatalf("Load(%s) = %v, want TLS_MATERIAL_INVALID", c.name, err)
			}
			if c.wantField != "" && fieldValue(err, "field") != c.wantField {
				t.Errorf("the field annotation is %q, want %q", fieldValue(err, "field"), c.wantField)
			}
			//: the file's CONTENTS must never reach the error, whatever it is.
			if strings.Contains(errs.PublicOf(err), "PRIVATE KEY") {
				t.Errorf("the public message quotes file contents: %q", errs.PublicOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("Load(%s) = %v, want nil", c.name, err)
		}
		if c.params.ServerName != "" {
			if got := id.ClientConfig().ServerName; got != c.params.ServerName {
				t.Errorf("ServerName = %q, want %q", got, c.params.ServerName)
			}
		}
		//: going through the filesystem must not create a second, unredacted
		//: path to the material.
		if id.String() != "<redacted>" || id.GoString() != "<redacted>" {
			t.Errorf("the loaded identity is not redacted: %s / %#v", id.String(), id)
		}
		//: and the modern floor must survive the disk round trip.
		if got := id.ClientConfig().MinVersion; got != tls.VersionTLS13 {
			t.Errorf("MinVersion = %x, want TLS 1.3", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// fieldValue returns the StringValue of the first field keyed key, or "".
func fieldValue(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		//: the first match wins; fields merge along the wrap chain.
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}
