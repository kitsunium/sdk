package consensus_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/consensus"
)

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// issue writes cert.pem and key.pem for a slot under dir, signed by the CA.
func issue(t *testing.T, dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, san *url.URL) [32]byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	writePEM(t, filepath.Join(dir, "cert.pem"), "CERTIFICATE", der)
	writePEM(t, filepath.Join(dir, "key.pem"), "EC PRIVATE KEY", kb)
	leaf, _ := x509.ParseCertificate(der)
	return consensus.SPKIOf(leaf)
}

// TestFacadeThreeNodesOverPEMAndDataDirs is the cross-process shape: data
// directories, PEM identities and the mTLS transport, through the facade.
func TestFacadeThreeNodesOverPEMAndDataDirs(t *testing.T) {
	root := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	caFile := filepath.Join(root, "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", caDER)
	cluster := consensus.ClusterID{0x42}

	type part struct {
		dir string
		dd  consensus.DataDir
		tr  *consensus.TCPTransport
		cfg consensus.Config
	}
	var parts []*part
	var members []consensus.Member
	for i := 0; i < 3; i++ {
		p := &part{dir: filepath.Join(root, fmt.Sprintf("n%d", i))}
		if err := os.MkdirAll(p.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		slot := fmt.Sprintf("site-%d", i)
		spki := issue(t, p.dir, ca, caKey, consensus.SAN(cluster, slot))
		dd, err := consensus.OpenDataDir(filepath.Join(p.dir, "data"))
		if err != nil {
			t.Fatal(err)
		}
		p.dd = dd
		tr, err := consensus.NewTCPTransportFromPEM(filepath.Join(p.dir, "cert.pem"), filepath.Join(p.dir, "key.pem"), caFile,
			consensus.TransportConfig{ListenAddress: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		p.tr = tr
		id, inc, err := consensus.Init(dd, cluster, slot)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, consensus.Member{ID: id, Incarnation: inc, Slot: slot, SPKI: spki, Address: tr.Addr(), Voter: true})
		parts = append(parts, p)
	}
	var nodes []*consensus.Node
	for _, p := range parts {
		p.cfg = consensus.Config{
			Cluster: cluster, Machine: &counter{}, Durability: consensus.DurabilityVolatile,
			Log: p.dd.Log(), Snapshots: p.dd.Snapshots(), Transport: p.tr,
			InitialTiming: consensus.TimingContract{
				HeartbeatInterval:  20 * time.Millisecond,
				ElectionTimeoutMin: 200 * time.Millisecond, ElectionTimeoutMax: 400 * time.Millisecond,
			},
			MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		}
		if err := consensus.Bootstrap(context.Background(), p.cfg, members); err != nil {
			t.Fatal(err)
		}
		n, err := consensus.NewRaft(p.cfg)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
		t.Cleanup(func() {
			_ = n.Close(context.Background())
			_ = p.tr.Close()
			_ = p.dd.Close()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; ; i++ {
		_, sum, err := consensus.ProposeAs[int](ctx, nodes[i%3], []byte("abcd"))
		if err == nil {
			if sum != 4 {
				t.Fatalf("sum %d", sum)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := consensus.Inspect(parts[0].dd); err == nil {
		t.Fatal("Inspect on a live directory")
	}
}
