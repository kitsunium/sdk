package consensus

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
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
	// tweak, when set, changes the node's configuration before it starts.
	tweak func(*Config)
}

func tcpConfig(cid coreconsensus.ClusterID, tn *tcpNode) Config {
	c := Config{
		Cluster: cid, Machine: tn.sm, Durability: DurabilityVolatile, Log: tn.log, Snapshots: tn.snaps,
		Transport: tn.tr, InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		MinVoters: 3, JoinAuthorizer: tn.authorizer(),
	}
	if tn.tweak != nil {
		tn.tweak(&c)
	}
	return c
}

func (tn *tcpNode) listen(t *testing.T, ca *testCA) {
	t.Helper()
	addr := tn.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		tr, err := NewTCPTransport(&TransportConfig{Certificate: tn.cert, Roots: ca.pool, ListenAddress: addr})
		if err == nil {
			tn.tr, tn.addr = tr, tr.Addr()
			return
		}
		if tn.addr == "" || !addrInUse(err) || time.Now().After(deadline) {
			t.Fatalf("listen on %s: %v (%s)", addr, err, fieldOf(err, "why"))
		}
	}
}

// addrInUse reports whether a transport could not bind because its address
// is still in use. A restart binds the address its previous transport just
// closed: on Solaris the port was not free again at once, and the
// restarted node's bind failed one run in a few (TRANSPORT_MISCONFIGURED).
// Probing the address first and binding after still lost that race on
// Solaris, so the bind itself is retried, as a restarted process would,
// for up to ten seconds; any other refusal fails at once.
func addrInUse(err error) bool {
	return strings.Contains(fieldOf(err, "why"), syscall.EADDRINUSE.Error())
}

// fieldOf is the string value of err's field key, "" when it has none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
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
	return newTCPClusterWith(t, nil)
}

// newTCPClusterWith is newTCPCluster with tweak applied to every founder's
// configuration.
func newTCPClusterWith(t *testing.T, tweak func(*Config)) (*testCA, coreconsensus.ClusterID, []*tcpNode) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	var ns []*tcpNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots(), tweak: tweak}
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
		if err := Bootstrap(context.Background(), new(tcpConfig(cid, tn)), members); err != nil {
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
	if _, err := NewTCPTransport(&TransportConfig{Certificate: c, Roots: ca.pool, ListenAddress: "127.0.0.1:0"}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("non-canonical SAN: %v", err)
	}
	// Session tickets stay off (R7-26).
	if !target.tr.id.ServerConfig(nil).SessionTicketsDisabled || !target.tr.id.ClientConfig(nil).SessionTicketsDisabled {
		t.Fatal("session tickets enabled")
	}
	if _, err := NewTCPTransport(&TransportConfig{Certificate: c, Roots: ca.pool, ListenAddress: "127.0.0.1:0", HandshakeTimeout: 2 * time.Second}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
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
	// A second JOIN for a slot with a live member is refused (R8-11) when it
	// names another node or another incarnation of it; the same identity
	// under the same key is the member itself asking again (#533).
	other := st
	other.ID++
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: other, Address: j.addr, Pins: pinsOf(ns)}); !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("a JOIN of another id on a held slot: %v", err)
	}
	other = st
	other.Incarnation[0]++
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: other, Address: j.addr, Pins: pinsOf(ns)}); !errors.Is(err, coreconsensus.JoinRefused) {
		t.Fatalf("a JOIN of another incarnation on a held slot: %v", err)
	}
	if _, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)}); err != nil {
		t.Fatalf("the member's own JOIN again: %v", err)
	}
}

// A joiner that restarts before anything reached its log JOINs again with
// its own stamp, is answered, and is fed: its transport admits the members
// only from a JOIN answer, and the slot it asks for is held by its own
// learner entry. It used to be refused "the slot still has a member" and
// to admit nobody, the leader's snapshot lane closed on every attempt
// (#533).
func TestAJoinerRestartedBeforeItsFirstInstallRejoins(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	proposeOnTCPLeader(t, ns, "a=1")
	j := &tcpNode{slot: "site-new", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	st := must(j.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A JOIN that met an election is tried again past the slot's rate
	// limit; the refusal of the member's own JOIN never went away.
	join := func() (err error) {
		for range 20 {
			if _, err = j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: []string{ns[0].addr, ns[1].addr, ns[2].addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns)}); err == nil {
				return nil
			}
			time.Sleep(150 * time.Millisecond)
		}
		return err
	}
	// The JOIN is acknowledged before the joiner's node runs: nothing the
	// leader sends reaches its log, as when it restarts mid-transfer.
	if err := join(); err != nil {
		t.Fatalf("first join: %v", err)
	}
	if _, ok := tcpLeader(t, ns).node.Membership().Member(id); !ok {
		t.Fatal("the learner is not listed")
	}
	if err := j.tr.Close(); err != nil {
		t.Fatal(err)
	}
	j.listen(t, ca) // the same address and certificate, a new process
	j.start(t, cid)
	defer j.stop(t)
	if err := join(); err != nil {
		t.Fatalf("the restarted joiner's JOIN: %v", err)
	}
	proposeOnTCPLeader(t, ns, "b=2")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, _ := j.sm.get("b"); v == "2" {
			return
		}
		if time.Now().After(deadline) {
			first, last := j.log.Bounds()
			t.Fatalf("the restarted joiner was never fed: %+v, log [%d,%d]", j.node.Status(), first, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// proposeOnTCPLeader proposes kv to whichever member leads now, again on a
// new leader when the one asked lost its leadership before answering: for a
// set the test may write twice, as proposeOnLeader's.
func proposeOnTCPLeader(t *testing.T, ns []*tcpNode, kv string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := tcpLeader(t, ns).node.Propose(ctx, []byte(kv))
		cancel()
		if err == nil {
			return
		}
		retry := errors.Is(err, coreconsensus.NotLeader) ||
			errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) || errs.HasCode(err, coreconsensus.CodeEntrySuperseded)
		if !retry || time.Now().After(deadline) {
			t.Fatalf("propose %q: %v", kv, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// R2-22: unauthenticated connections are capped per address before any TLS
// byte is read; a member's address keeps its reserved slots.
func TestTCPCapsUnauthenticatedConnectionsPerAddress(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xab}
	cert, _ := ca.issue(t, SAN(cid, "a"), nil)
	tr, err := NewTCPTransport(&TransportConfig{
		Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0",
		MaxUnauthenticatedPerIP: 2, ReservedMemberSlots: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// raw is how many admitted connections still wait on their handshake.
	raw := func() int {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return len(tr.raw)
	}
	// until polls cond, within a bound far beyond any accept loop's pace.
	until := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		//: until the state is observed.
		for !cond() {
			//: past the bound, the state never came.
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// open dials n connections that send nothing, and returns once the
	// accept loop has decided each one: admitted (still raw, its handshake
	// unread — its deadline is a second away) or refused (counted).
	open := func(n int) []net.Conn {
		before := tr.RefusedBeforeHandshake()
		var cs []net.Conn
		for range n {
			c, err := net.Dial("tcp", tr.Addr())
			if err != nil {
				t.Fatal(err)
			}
			cs = append(cs, c)
		}
		until("every connection admitted or refused", func() bool {
			return int64(raw())+tr.RefusedBeforeHandshake()-before >= int64(n)
		})
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
	// A closed client ends its handshake at once: each slot is given back
	// before its connection leaves the raw set.
	until("the closed connections' slots given back", func() bool { return raw() == 0 })
	tr.SetMembership(1, []coreconsensus.MemberValue{{ID: 1, SPKI: [32]byte{1}, Address: tr.Addr()}})
	// The reservation resolves the members' addresses on a goroutine of its
	// own: wait until the admission gives the loopback more than the
	// unauthenticated cap — three slots at once — before dialling.
	loopback := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	until("the member's address reserved", func() bool {
		var releases []func()
		reserved := true
		//: three admissions over a cap of two say the reservation holds.
		for range 3 {
			release, ok := tr.admission.Admit(loopback)
			//: refused: not reserved yet.
			if !ok {
				reserved = false
				break
			}
			releases = append(releases, release)
		}
		//: the probe's slots are given back whatever it saw.
		for _, release := range releases {
			release()
		}
		return reserved
	})
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

// A link whose connection died while it was quiet delivers the next frame
// on a new connection instead of dropping it. The chaos run's survivors of
// a leader kill stayed pre-candidates for 15 s: their links to each other
// had carried nothing since a partition, and each vote went into a stream
// that only reached the peer at a retransmission. The connection here is
// reset by its receiver, the state the kernel leaves a link in once
// boundUnacknowledged aborts it.
func TestTCPFrameOnALinkThatDiedQuietlyIsDelivered(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	a, b := &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	var members []coreconsensus.MemberValue
	for _, tn := range []*tcpNode{a, b} {
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		tn.listen(t, ca)
		defer tn.tr.Close()
		members = append(members, coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	got := make(chan struct{}, 8)
	if err := b.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) { got <- struct{}{} }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.tr.SetMembership(1, members)
	b.tr.SetMembership(1, members)
	frame := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgPreVote, From: a.id, To: b.id, Term: 2})
	received := func(what string) {
		t.Helper()
		select {
		case <-got:
		case <-time.After(time.Second):
			t.Fatalf("%s: the frame never arrived", what)
		}
	}
	a.tr.Send(b.id, frame)
	received("first frame")

	// The receiver resets every connection it holds, then waits for the
	// reset to reach the sender.
	b.tr.mu.Lock()
	for c := range b.tr.in {
		tc := c.(*tls.Conn).NetConn().(*net.TCPConn)
		if err := tc.SetLinger(0); err != nil {
			t.Error(err)
		}
		abandon(tc.Close())
	}
	b.tr.mu.Unlock()
	time.Sleep(100 * time.Millisecond)

	a.tr.Send(b.id, frame)
	received("frame after the reset")
}

// A chunk length is compared before its conversion to int: on 32 bits a
// length of 2^31 or more turned negative, passed the bound and panicked
// make in the serving goroutine. The test-386 lane runs this (#422).
func TestReadChunkRefusesALengthAboveTheLimit(t *testing.T) {
	for _, ln := range []uint32{0xFFFFFFFF, 1 << 31, 513} {
		var h [4]byte
		binary.LittleEndian.PutUint32(h[:], ln)
		if _, err := readChunk(bytes.NewReader(h[:]), 512); !errors.Is(err, coreconsensus.FrameInvalid) {
			t.Fatalf("length %#x: %v, want FRAME_INVALID", ln, err)
		}
	}
}

// A frame length is bounded before the header's 4 bytes are added to it as
// an int: on 32 bits a length within 4 of 2^31 wrapped the sum negative,
// passed the limit, and the slice panicked the reader of a member's
// connection, and the node with it. The test-386 lane runs this.
func TestReadFrameRefusesALengthThatWrapsOn32Bits(t *testing.T) {
	for _, ln := range []uint32{0x7FFFFFFC, 0x7FFFFFFF, 1 << 31, 0xFFFFFFFF} {
		var h [4]byte
		binary.LittleEndian.PutUint32(h[:], ln)
		if _, _, ok := readFrame(bytes.NewReader(h[:]), nil, 1<<20); ok {
			t.Fatalf("length %#x read under a limit of 1 MiB", ln)
		}
	}
}

// failingListener fails its first fails Accepts the way an exhausted
// descriptor table does, then accepts from the listener it wraps.
type failingListener struct {
	net.Listener
	fails atomic.Int32
}

func (l *failingListener) Accept() (net.Conn, error) {
	if l.fails.Add(-1) >= 0 {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	}
	return l.Listener.Accept()
}

// #518: an Accept that fails without the listener being closed — EMFILE,
// ENFILE — is waited out, never the end of the accept loop. It ended it,
// and the node, its port still open, never received a frame again.
func TestTCPAcceptSurvivesATransientFailure(t *testing.T) {
	type tc struct {
		name string
		// fails is how many Accepts in a row fail before one succeeds.
		fails int32
	}
	runCase := func(t *testing.T, c tc) {
		listenTCP = func(network, addr string) (net.Listener, error) {
			ln, err := net.Listen(network, addr)
			if err != nil {
				return nil, err
			}
			fl := &failingListener{Listener: ln}
			fl.fails.Store(c.fails)
			return fl, nil
		}
		t.Cleanup(func() { listenTCP = net.Listen })
		acceptAfterFailures(t, c.fails)
	}
	for _, c := range []tc{{name: "one failure", fails: 1}, {name: "a run of failures", fails: 8}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// acceptAfterFailures starts two transports, the receiver's listener
// failing its first fails Accepts, and requires a frame to reach it.
func acceptAfterFailures(t *testing.T, fails int32) {
	t.Helper()
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	a, b := &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	var members []coreconsensus.MemberValue
	for _, tn := range []*tcpNode{a, b} {
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		tn.listen(t, ca)
		defer tn.tr.Close()
		members = append(members, coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	got := make(chan struct{}, 64)
	if err := b.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) { got <- struct{}{} }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.tr.SetMembership(1, members)
	b.tr.SetMembership(1, members)
	frame := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgPreVote, From: a.id, To: b.id, Term: 2})
	deadline := time.After(10 * time.Second)
	for {
		a.tr.Send(b.id, frame)
		select {
		case <-got:
			return
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatalf("after %d failed Accepts the transport never received a frame again", fails)
		}
	}
}
