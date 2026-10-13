package consensus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "consensus test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &testCA{cert: c, key: key, pool: pool}
}

func (ca *testCA) issue(t *testing.T, san *url.URL, key *ecdsa.PrivateKey) (*tls.Certificate, [32]byte) {
	t.Helper()
	if key == nil {
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, SPKIOf(leaf)
}

type tcpNode struct {
	id    coreconsensus.NodeID
	inc   coreconsensus.Incarnation
	slot  string
	cert  *tls.Certificate
	spki  [32]byte
	addr  string
	tr    *TCPTransport
	log   *MemoryLog
	snaps *MemorySnapshots
	sm    *kv
	node  *Node
}

func tcpConfig(cid coreconsensus.ClusterID, tn *tcpNode) Config {
	return Config{Cluster: cid, Machine: tn.sm, Durability: DurabilityVolatile, Log: tn.log, Snapshots: tn.snaps,
		Transport: tn.tr, InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		MinVoters: 3, JoinAuthorizer: allowJoin{}}
}

func (tn *tcpNode) listen(t *testing.T, ca *testCA) {
	t.Helper()
	addr := tn.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	tr, err := NewTCPTransport(TransportConfig{Certificate: tn.cert, Roots: ca.pool, ListenAddress: addr})
	if err != nil {
		t.Fatal(err)
	}
	tn.tr, tn.addr = tr, tr.Addr()
}

func (tn *tcpNode) start(t *testing.T, cid coreconsensus.ClusterID) {
	t.Helper()
	tn.sm = newKV()
	n, err := NewRaft(tcpConfig(cid, tn))
	if err != nil {
		t.Fatal(err)
	}
	tn.node = n
}

func (tn *tcpNode) stop() {
	if tn.node != nil {
		_ = tn.node.HardClose()
		tn.node = nil
	}
	if tn.tr != nil {
		_ = tn.tr.Close()
	}
}

func tcpLeader(t *testing.T, ns []*tcpNode) *tcpNode {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, tn := range ns {
			if tn.node != nil && tn.node.Status().Role == coreconsensus.RoleLeader {
				return tn
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no leader over TCP")
	return nil
}

func newTCPCluster(t *testing.T) (*testCA, coreconsensus.ClusterID, []*tcpNode) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	var ns []*tcpNode
	var members []coreconsensus.Member
	for i := 0; i < 3; i++ {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots()}
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		tn.listen(t, ca)
		id, inc, err := InitLog(tn.log, cid, tn.slot)
		if err != nil {
			t.Fatal(err)
		}
		tn.id, tn.inc = id, inc
		ns = append(ns, tn)
		members = append(members, coreconsensus.Member{ID: id, Incarnation: inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	for _, tn := range ns {
		tn.sm = newKV()
		if err := Bootstrap(context.Background(), tcpConfig(cid, tn), members); err != nil {
			t.Fatal(err)
		}
		tn.start(t, cid)
	}
	t.Cleanup(func() {
		for _, tn := range ns {
			tn.stop()
		}
	})
	return ca, cid, ns
}

func TestTCPClusterOverMutualTLS(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	tok := propose(t, l.node, "a=1")
	var f *tcpNode
	for _, tn := range ns {
		if tn != l {
			f = tn
		}
	}
	propose(t, f.node, "b=2")

	// Kill the leader; the other two elect and commit; the old one comes
	// back on the same address and catches up.
	l.stop()
	l2 := tcpLeader(t, ns)
	tok2 := propose(t, l2.node, "c=3")
	if tok2.Index <= tok.Index {
		t.Fatal("index went back")
	}
	l.listen(t, ca)
	l.start(t, cid)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, _ := l.sm.get("c"); v == "3" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted node did not catch up over TCP: %+v", l.node.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A certificate for a member's slot over ANOTHER key is refused, even
// valid (R8-07); a certificate of another cluster is refused.
func TestTCPRefusesAnUnpinnedKeyAndAnotherCluster(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	tcpLeader(t, ns)
	target := ns[0]

	impostor := &tcpNode{slot: target.slot}
	impostor.cert, _ = ca.issue(t, SAN(cid, target.slot), nil)
	impostor.listen(t, ca)
	defer impostor.tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Dialing a member under the wrong pin is refused by the dialer.
	wrong := [32]byte{1}
	if _, err := impostor.tr.dial(ctx, target.addr, laneControl, &wrong); err == nil {
		t.Fatal("dial accepted a listener whose key is not the pinned one")
	}
	// Frames from the impostor's key are never delivered.
	before := target.node.Status().Term
	impostor.tr.SetMembership(1, []coreconsensus.Member{{ID: target.id, SPKI: target.spki, Address: target.addr}})
	m := coreconsensus.Message{Version: 1, Group: 1, Kind: coreconsensus.MsgVote, From: ns[1].id, To: target.id, Term: before + 10, LogTerm: 99, Index: 99}
	for i := 0; i < 5; i++ {
		impostor.tr.Send(target.id, appendFrame(&m))
	}
	time.Sleep(300 * time.Millisecond)
	if got := target.node.Status().Term; got >= before+10 {
		t.Fatalf("a frame over an unpinned key moved the term to %d", got)
	}

	other := coreconsensus.ClusterID{0xba, 0xd}
	stranger := &tcpNode{slot: "x"}
	stranger.cert, _ = ca.issue(t, SAN(other, "x"), nil)
	stranger.listen(t, ca)
	defer stranger.tr.Close()
	if _, err := stranger.tr.dial(ctx, target.addr, laneControl, &target.spki); err == nil {
		t.Fatal("a certificate of another cluster was accepted")
	}

	// A non-canonical SAN is refused at construction.
	bad := &url.URL{Scheme: "kitsunium-consensus", Host: cid.String(), Path: "/slot/x/"}
	c, _ := ca.issue(t, bad, nil)
	if _, err := NewTCPTransport(TransportConfig{Certificate: c, Roots: ca.pool, ListenAddress: "127.0.0.1:0"}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("non-canonical SAN: %v", err)
	}
	// Session tickets stay off (R7-26).
	if !target.tr.tlsConfig().SessionTicketsDisabled {
		t.Fatal("session tickets enabled")
	}
	if _, err := NewTCPTransport(TransportConfig{Certificate: c, Roots: ca.pool, ListenAddress: "127.0.0.1:0", HandshakeTimeout: 2 * time.Second}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("handshake timeout above 1 s: %v", err)
	}
}

func TestTCPJoinAddsALearner(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	propose(t, l.node, "a=1")
	j := &tcpNode{slot: "site-new", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	j.start(t, cid)
	defer j.stop()
	st, _ := j.log.Stamp()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pos, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr, ns[1].addr}, Stamp: st, Address: j.addr})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if pos.Index == 0 {
		t.Fatal("no position")
	}
	m := l.node.Membership()
	jm, ok := m.Member(id)
	if !ok || jm.Voter || jm.SPKI != j.spki {
		t.Fatalf("joiner not admitted as a pinned learner: %+v", jm)
	}
	propose(t, l.node, "b=2")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, _ := j.sm.get("b"); v == "2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("learner did not replicate over TCP: %+v", j.node.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A second JOIN for a slot with a live member is refused (R8-11).
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: st, Address: j.addr}); !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("second join: %v", err)
	}
}

// R2-22: unauthenticated connections are capped per address before any TLS
// byte is read; a member's address keeps its reserved slots.
func TestTCPCapsUnauthenticatedConnectionsPerAddress(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xab}
	cert, _ := ca.issue(t, SAN(cid, "a"), nil)
	tr, err := NewTCPTransport(TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0",
		MaxUnauthenticatedPerIP: 2, ReservedMemberSlots: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	open := func(n int) []net.Conn {
		var cs []net.Conn
		for i := 0; i < n; i++ {
			c, err := net.Dial("tcp", tr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			cs = append(cs, c)
		}
		time.Sleep(50 * time.Millisecond)
		return cs
	}
	closeAll := func(cs []net.Conn) {
		for _, c := range cs {
			c.Close()
		}
	}
	cs := open(4)
	if got := tr.RefusedBeforeHandshake(); got != 2 {
		t.Fatalf("refused %d of 4 over a cap of 2", got)
	}
	closeAll(cs)
	time.Sleep(1200 * time.Millisecond) // the handshake timeout frees the slots
	tr.SetMembership(1, []coreconsensus.Member{{ID: 1, SPKI: [32]byte{1}, Address: tr.Addr()}})
	before := tr.RefusedBeforeHandshake()
	cs = open(4)
	defer closeAll(cs)
	if got := tr.RefusedBeforeHandshake() - before; got != 0 {
		t.Fatalf("a member's address lost %d of its reserved slots", got)
	}
}
