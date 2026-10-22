package consensus_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/pkg/v1/app/consensus"
)

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

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// issue writes cert.pem and key.pem for a slot under dir, signed by the CA.
func issue(t *testing.T, dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, san *url.URL) [32]byte {
	t.Helper()
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	serial := must(rand.Int(rand.Reader, big.NewInt(1<<62)))(t)
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb := must(x509.MarshalECPrivateKey(key))(t)
	writePEM(t, filepath.Join(dir, "cert.pem"), "CERTIFICATE", der)
	writePEM(t, filepath.Join(dir, "key.pem"), "EC PRIVATE KEY", kb)
	leaf := must(x509.ParseCertificate(der))(t)
	return consensus.SPKIOf(leaf)
}

// closeStackAtCleanup tears one node's stack down when the test ends, node
// first, then its transport, then its data directory — the reverse of the
// order they were opened — and reports every step that failed rather than
// letting a broken shutdown pass unseen.
func closeStackAtCleanup(t *testing.T, n closer, tr, dd io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := n.Close(context.Background()); err != nil {
			t.Errorf("close node: %v", err)
		}
		if err := tr.Close(); err != nil {
			t.Errorf("close transport: %v", err)
		}
		if err := dd.Close(); err != nil {
			t.Errorf("close data directory: %v", err)
		}
	})
}

// TestFacadeThreeNodesOverPEMAndDataDirs is the cross-process shape: data
// directories, PEM identities and the mTLS transport, through the facade.
func TestFacadeThreeNodesOverPEMAndDataDirs(t *testing.T) {
	root := t.TempDir()
	caKey := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, IsCA: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER := must(x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey))(t)
	ca := must(x509.ParseCertificate(caDER))(t)
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
	var members []consensus.MemberValue
	for i := range 3 {
		p := &part{dir: filepath.Join(root, fmt.Sprintf("n%d", i))}
		if err := os.MkdirAll(p.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		slot := fmt.Sprintf("site-%d", i)
		spki := issue(t, p.dir, ca, caKey, consensus.SAN(cluster, slot))
		// The boot-scoped marker directory under the test's own root: /run
		// is root's, and the data directory refuses one it cannot write.
		dd, err := consensus.NewDataDirWith(filepath.Join(p.dir, "data"), consensus.DataDirOptions{RuntimeDir: filepath.Join(root, "run")})
		//: illumos and Solaris have no flock(2), so no data directory (ADR
		//: 0144): the refusal is asserted, then the case skips.
		if runtime.GOOS == "illumos" || runtime.GOOS == "solaris" {
			if !errors.Is(err, coreproc.UnsupportedPlatform) {
				t.Fatalf("NewDataDirWith on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
			}
			t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144); the refusal is asserted above", runtime.GOOS)
		}
		if err != nil {
			t.Fatal(err)
		}
		p.dd = dd
		tr, err := consensus.NewTCPTransportFromPEM(filepath.Join(p.dir, "cert.pem"), filepath.Join(p.dir, "key.pem"), caFile,
			&consensus.TransportConfig{ListenAddress: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		p.tr = tr
		id, inc, err := consensus.Init(dd, cluster, slot)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, consensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: spki, Address: tr.Addr(), Voter: true})
		parts = append(parts, p)
	}
	var nodes []*consensus.Node
	for _, p := range parts {
		p.cfg = consensus.Config{
			Cluster: cluster, Machine: &counter{}, Durability: consensus.DurabilityVolatile,
			Log: p.dd.Log(), Snapshots: p.dd.Snapshots(), Transport: p.tr,
			InitialTiming: consensus.TimingContractSpec{
				HeartbeatInterval:  20 * time.Millisecond,
				ElectionTimeoutMin: 200 * time.Millisecond, ElectionTimeoutMax: 400 * time.Millisecond,
			},
			MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		}
		if err := consensus.Bootstrap(context.Background(), &p.cfg, members); err != nil {
			t.Fatal(err)
		}
		n, err := consensus.NewNode(p.cfg)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
		closeStackAtCleanup(t, n, p.tr, p.dd)
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
