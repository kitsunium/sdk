package consensus

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// writePEM writes the PEM blocks of type typ, one per der, to path.
func writePEM(t *testing.T, path, typ string, der ...[]byte) {
	t.Helper()
	var b []byte
	for _, d := range der {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: d})...)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The PEM transport admits a peer of a CA rollover's new CA once the bundle
// naming both lands, though it was copied with its source's modification
// time kept (cp -p, rsync -a, tar x), older than the certificate's (#633).
// The transport verifies through its identity's live roots, and the
// identity reads a file whose bytes changed whatever its time. Before, the
// running node refused every peer of the new CA until it restarted, and
// ReloadFailures stayed 0.
func TestPEMTransportTrustsARolloverBundleCopiedWithItsTimeKept(t *testing.T) {
	cid := coreconsensus.ClusterID{0xa7}
	oldCA, newCA := newTestCA(t), newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key"), filepath.Join(dir, "ca.pem")
	certA, spkiA := oldCA.issue(t, SAN(cid, "a"), nil)
	writePEM(t, certFile, "CERTIFICATE", certA.Certificate[0])
	writePEM(t, keyFile, "EC PRIVATE KEY", must(x509.MarshalECPrivateKey(certA.PrivateKey.(*ecdsa.PrivateKey)))(t))
	writePEM(t, caFile, "CERTIFICATE", oldCA.cert.Raw)
	trA, err := NewTCPTransportFromPEM(certFile, keyFile, caFile, &TransportConfig{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { abandon(trA.Close()) })
	got := make(chan struct{}, 1)
	if err := trA.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {
		select {
		case got <- struct{}{}:
		default:
		}
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Step 1 of the rollover on A: the bundle lands with the time its copy
	// kept, a day ago. A look past the identity's Every, one second, as
	// every handshake makes, reads it.
	writePEM(t, caFile, "CERTIFICATE", oldCA.cert.Raw, newCA.cert.Raw)
	dayAgo := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(caFile, dayAgo, dayAgo); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	_ = trA.CertExpiry()

	// B, certified by the new CA, trusts both, as the rollover deploys.
	both := x509.NewCertPool()
	both.AddCert(oldCA.cert)
	both.AddCert(newCA.cert)
	certB, spkiB := newCA.issue(t, SAN(cid, "b"), nil)
	trB, err := NewTCPTransport(&TransportConfig{Certificate: certB, Roots: both, ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { abandon(trB.Close()) })
	members := []coreconsensus.MemberValue{
		{ID: 1, Slot: "a", SPKI: spkiA, Address: trA.Addr(), Voter: true},
		{ID: 2, Slot: "b", SPKI: spkiB, Address: trB.Addr(), Voter: true},
	}
	trA.SetMembership(1, members)
	trB.SetMembership(1, members)
	hb := wire.AppendMessage(nil, &coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgHeartbeat, From: 2, To: 1, Term: 1})
	for deadline := time.Now().Add(5 * time.Second); ; {
		trB.Send(1, hb)
		select {
		case <-got:
			if n := trA.ReloadFailures(); n != 0 {
				t.Fatalf("reload failures %d, want 0", n)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("A never admits B, certified by the new CA its roots file now names (reload failures %d)", trA.ReloadFailures())
		}
	}
}
