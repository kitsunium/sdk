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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "consensus test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c := must(x509.ParseCertificate(der))(t)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &testCA{cert: c, key: key, pool: pool}
}

func (ca *testCA) issue(t *testing.T, san *url.URL, key *ecdsa.PrivateKey) (*tls.Certificate, [32]byte) {
	t.Helper()
	if key == nil {
		key = must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t)
	}
	serial := must(rand.Int(rand.Reader, big.NewInt(1<<62)))(t)
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := must(x509.ParseCertificate(der))(t)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, SPKIOf(leaf)
}

type tcpNode struct {
	auth  coreconsensus.JoinAuthorizer
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
	return Config{
		Cluster: cid, Machine: tn.sm, Durability: DurabilityVolatile, Log: tn.log, Snapshots: tn.snaps,
		Transport: tn.tr, InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		MinVoters: 3, JoinAuthorizer: tn.authorizer(),
	}
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
	n, err := NewNode(tcpConfig(cid, tn))
	if err != nil {
		t.Fatal(err)
	}
	tn.node = n
}

// stop cuts the node and its transport, reporting either step that failed:
// a transport that cannot close leaves its listener bound and a later restart
// on the same address would test the wrong thing.
func (tn *tcpNode) stop(t *testing.T) {
	t.Helper()
	if tn.node != nil {
		if err := tn.node.HardClose(); err != nil {
			t.Errorf("hard close %d: %v", tn.id, err)
		}
		tn.node = nil
	}
	if tn.tr != nil {
		if err := tn.tr.Close(); err != nil {
			t.Errorf("close transport %d: %v", tn.id, err)
		}
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
	var members []coreconsensus.MemberValue
	for i := range 3 {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots()}
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		tn.listen(t, ca)
		id, inc, err := InitLog(tn.log, cid, tn.slot)
		if err != nil {
			t.Fatal(err)
		}
		tn.id, tn.inc = id, inc
		ns = append(ns, tn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
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
			tn.stop(t)
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
	l.stop(t)
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
	wrong := &[32]byte{1}
	if _, err := impostor.tr.dial(ctx, target.addr, laneControl, wrong); err == nil {
		t.Fatal("dial accepted a listener whose key is not the pinned one")
	}
	// Frames from the impostor's key are never delivered.
	before := target.node.Status().Term
	impostor.tr.SetMembership(1, []coreconsensus.MemberValue{{ID: target.id, SPKI: target.spki, Address: target.addr}})
	m := coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgVote, From: ns[1].id, To: target.id, Term: before + 10, LogTerm: 99, Index: 99}
	for range 5 {
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
	if !target.tr.id.ServerConfig(nil).SessionTicketsDisabled || !target.tr.id.ClientConfig(nil).SessionTicketsDisabled {
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
	defer j.stop(t)
	st := must(j.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// An answer from a peer the joiner does not pin counts for nothing,
	// even from a valid certificate of the cluster (review M4).
	impostor := &tcpNode{slot: "site-x"}
	impostor.cert, _ = ca.issue(t, SAN(cid, impostor.slot), nil)
	impostor.listen(t, ca)
	defer impostor.tr.Close()
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{impostor.addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)}); err == nil {
		t.Fatal("a JOIN answered by an unpinned peer")
	}
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: st, Address: j.addr}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("a JOIN with no pin: %v", err)
	}
	pos, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{impostor.addr, ns[0].addr, ns[1].addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)})
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
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)}); !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("second join: %v", err)
	}
}

// R2-22: unauthenticated connections are capped per address before any TLS
// byte is read; a member's address keeps its reserved slots.
func TestTCPCapsUnauthenticatedConnectionsPerAddress(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xab}
	cert, _ := ca.issue(t, SAN(cid, "a"), nil)
	tr, err := NewTCPTransport(TransportConfig{
		Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0",
		MaxUnauthenticatedPerIP: 2, ReservedMemberSlots: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	open := func(n int) []net.Conn {
		var cs []net.Conn
		for range n {
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
	tr.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: [32]byte{1}, Address: tr.Addr()}})
	before := tr.RefusedBeforeHandshake()
	cs = open(4)
	defer closeAll(cs)
	if got := tr.RefusedBeforeHandshake() - before; got != 0 {
		t.Fatalf("a member's address lost %d of its reserved slots", got)
	}
}

func (tn *tcpNode) authorizer() coreconsensus.JoinAuthorizer {
	if tn.auth != nil {
		return tn.auth
	}
	return allowJoin{}
}

// authorizerReason is an error written the way a foreign authorizer writes
// one — a sentence about its own world, proper nouns capitalised — rather
// than in Go's error style: the test pins that such a reason reaches the
// joiner byte for byte, so the casing is part of the data under test.
type authorizerReason string

func (r authorizerReason) Error() string { return string(r) }

// denyJoin refuses every JOIN with its error.
type denyJoin struct{ err error }

func (d denyJoin) Authorize(context.Context, coreconsensus.JoinRequestValue) (coreconsensus.Operator, error) {
	return "", d.err
}

// joinWith restarts every member under auth, then has a new node of a new
// slot JOIN them over TCP, and returns the joiner's error.
func joinWith(t *testing.T, auth coreconsensus.JoinAuthorizer) error {
	t.Helper()
	ca, cid, ns := newTCPCluster(t)
	for _, tn := range ns {
		if err := tn.node.HardClose(); err != nil {
			t.Fatal(err)
		}
		tn.auth = auth
		tn.start(t, cid)
	}
	tcpLeader(t, ns)
	j := &tcpNode{slot: "site-new", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	defer j.tr.Close()
	if _, _, err := InitLog(j.log, cid, j.slot); err != nil {
		t.Fatal(err)
	}
	st := must(j.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)})
	return err
}

// The authorizer's reason reaches the joiner over the wire, sanitized and
// bounded; an authorizer that says nothing leaves no reason.
func TestJoinRefusalCarriesTheAuthorizersReason(t *testing.T) {
	err := joinWith(t, denyJoin{authorizerReason("Nomad node wkld-3 is draining")})
	if !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("refusal: %v", err)
	}
	if r, ok := JoinRefusalReason(err); !ok || r != "Nomad node wkld-3 is draining" {
		t.Fatalf("reason %q %v", r, ok)
	}

	long := "a\x00b\x1b[31m" + strings.Repeat("é", 300) // control bytes, then 600 bytes
	err = joinWith(t, denyJoin{errors.New(long)})
	r, ok := JoinRefusalReason(err)
	if !ok || len(r) > MaxJoinReasonBytes || !utf8.ValidString(r) || strings.ContainsAny(r, "\x00\x1b") || !strings.HasPrefix(r, "ab[31m") {
		t.Fatalf("long reason not bounded and sanitized: %d bytes %q", len(r), r)
	}

	err = joinWith(t, denyJoin{errors.New("")})
	if !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("silent refusal: %v", err)
	}
	if r, ok := JoinRefusalReason(err); ok {
		t.Fatalf("an authorizer that said nothing produced a reason %q", r)
	}
}

// An SDK error from the authorizer gives its PUBLIC message only: its
// private text and its fields stay on the member.
func TestJoinReasonOfAnSDKErrorIsItsPublicMessage(t *testing.T) {
	err := errs.Wrap(coreconsensus.JoinRefused, errs.WrapParams{}, errs.String("internal", "10.10.0.21:4646"))
	got := joinReasonOf(err)
	if got != errs.PublicOf(err) || strings.Contains(got, "10.10.0.21") {
		t.Fatalf("reason %q", got)
	}
	if joinReasonOf(errors.New(" \t ")) != "" {
		t.Fatal("blank text made a reason")
	}
}

// A key two members share admits neither: the transport could not tell
// which member a connection speaks for (review M5).
func TestTCPAdmitsNoKeyTwoMembersShare(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	tn := &tcpNode{slot: "site-0"}
	tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
	tn.listen(t, ca)
	defer tn.tr.Close()
	shared := [32]byte{7}
	tn.tr.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: shared}, {ID: 2, SPKI: shared}, {ID: 3, SPKI: [32]byte{3}}})
	tn.tr.mu.Lock()
	_, admitted := tn.tr.bySPKI[shared]
	_, other := tn.tr.bySPKI[[32]byte{3}]
	tn.tr.mu.Unlock()
	if admitted || !other {
		t.Fatalf("shared key admitted=%v, distinct key admitted=%v", admitted, other)
	}
}

// pinsOf lists the members' keys, as an operator hands them to a joiner.
func pinsOf(ns []*tcpNode) [][32]byte {
	out := make([][32]byte, len(ns))
	for i, tn := range ns {
		out[i] = tn.spki
	}
	return out
}

// An answer whose membership does not list the responder under its key is
// a forged one: the joiner does not install it (review M4).
func TestJoinAnswerMustListTheResponder(t *testing.T) {
	if joinAnswerListsPeer(coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{{ID: 1, SPKI: [32]byte{1}}}}, [32]byte{2}) {
		t.Fatal("a membership without the responder's key was accepted")
	}
	if !joinAnswerListsPeer(coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{{ID: 1, SPKI: [32]byte{1}}}}, [32]byte{1}) {
		t.Fatal("a membership listing the responder was refused")
	}
}
