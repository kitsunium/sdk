package consensus

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
	"github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/internal/service/net/tlsid"
)

// The lane is the first byte a dialer writes after the handshake: it says
// what the connection carries, so control traffic never queues behind
// entries or a snapshot (D4).
const (
	// laneControl carries votes and heartbeats, under the smaller frame
	// bound.
	laneControl byte = 0
	// laneData carries entries.
	laneData byte = 1
	// laneSnapshot carries one snapshot offer and its stream.
	laneSnapshot byte = 2
	// laneJoin carries one JOIN from a node that is not a member yet.
	laneJoin byte = 3

	// sanScheme is the URI scheme of a node certificate's one URI SAN.
	sanScheme = "kitsunium-consensus"
)

const (
	// defaultUnauthenticatedPerIP is the pre-handshake budget of one
	// address, the one server.Admission defaults to (ADR 0174 D2).
	defaultUnauthenticatedPerIP int = 4
	// defaultReservedMemberSlots is the budget of a member's address, the
	// reserved one server.Admission defaults to (ADR 0174 D2).
	defaultReservedMemberSlots int = 8
	// minFrameBody is the smallest body a frame can announce: its version,
	// kind and group. A shorter one is refused before any allocation.
	minFrameBody int = 10
	// senderQueueFrames bounds the frames queued for one peer and lane; the
	// byte bound PeerQueueBytes usually binds first.
	senderQueueFrames int = 4096
	// maxSenderBatch caps the frames one write carries, so a long queue
	// is flushed in bounded writes under one deadline each.
	maxSenderBatch int = 1024
	// redialBackoff is how long a sender drops frames after a failed dial
	// instead of dialling again for each: Raft retransmits what matters.
	redialBackoff time.Duration = 100 * time.Millisecond
	// connBufferBytes sizes the buffered reader and writer of a connection.
	connBufferBytes int = 64 << 10
	// acceptBackoffFirst is the wait after the first of consecutive failed
	// Accepts; each next one doubles it (#518).
	acceptBackoffFirst time.Duration = 5 * time.Millisecond
	// acceptBackoffCeiling bounds the wait after a failed Accept.
	acceptBackoffCeiling time.Duration = time.Second
)

// listenTCP opens the transport's listener; a test replaces it to make
// Accept fail the way an exhausted descriptor table does (#518).
var listenTCP = net.Listen

// TransportConfig builds the shipped mTLS transport (D4, D14). A zero bound
// is clamped to its default; nil Roots, no certificate, a HandshakeTimeout
// above one second, or a control frame bound not below the data bound is
// refused.
type TransportConfig struct {
	// Certificate is the node's certificate and key. It carries exactly one
	// URI SAN, kitsunium-consensus://<cluster>/slot/<slot>, and both
	// serverAuth and clientAuth; the transport takes its cluster and slot
	// from it. GetCertificate, when set, is asked at every handshake instead,
	// so a renewed certificate over the same key is picked up in place.
	Certificate    *tls.Certificate
	GetCertificate func() (*tls.Certificate, error)
	// Roots is the consensus intermediate, trusted as RootCAs and ClientCAs.
	Roots *x509.CertPool
	// ListenAddress is bound as given, e.g. a private interface (R2-37).
	ListenAddress string
	// Seeds are addresses for the first contact; the committed membership's
	// addresses rule afterwards.
	Seeds map[coreconsensus.NodeID]string

	// MaxUnauthenticatedPerIP caps the connections an IPv4 address, or an
	// IPv6 /64, may hold before their handshake completes; the addresses
	// the admitted members' addresses resolve to get ReservedMemberSlots
	// instead, so a flood never locks a member out (R2-22). It runs before
	// any TLS byte is read.
	MaxUnauthenticatedPerIP int
	ReservedMemberSlots     int

	MaxFrameBytes        int
	MaxControlFrameBytes int
	HandshakeTimeout     time.Duration
	PeerQueueBytes       int64
	MaxConnectionAge     time.Duration
	WriteTimeout         time.Duration
	JoinTimeout          time.Duration
}

// transportMisconfigured refuses a TransportConfig, naming the field and
// the reason, never the value.
func transportMisconfigured(field, why string) error {
	return origin(coreconsensus.TransportMisconfigured, errs.String("field", field), errs.String("why", why))
}

// peerRejected refuses a peer, saying why without echoing what it sent.
func peerRejected(why string) error {
	return origin(coreconsensus.PeerRejected, errs.String("why", why))
}

// parseSAN reads the one URI SAN in its byte-exact canonical form (R7-25,
// R7-41): lowercase scheme, no user, port, query, fragment or trailing
// slash, the cluster in lowercase hex, the slot in [a-z0-9-]{1,32}.
func parseSAN(c *x509.Certificate) (coreconsensus.ClusterID, string, error) {
	var cid coreconsensus.ClusterID
	if len(c.URIs) != 1 {
		return cid, "", peerRejected("not exactly one URI SAN")
	}
	u := c.URIs[0]
	if u.Scheme != sanScheme || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return cid, "", peerRejected("URI SAN form")
	}
	cid, err := sanCluster(u.Host)
	if err != nil {
		return cid, "", err
	}
	slot, ok := strings.CutPrefix(u.Path, "/slot/")
	if !ok || !validSlot(slot) || u.String() != sanScheme+"://"+u.Host+"/slot/"+slot {
		return cid, "", peerRejected("URI SAN slot")
	}
	return cid, slot, nil
}

// sanCluster reads the cluster of a URI SAN's host: exactly a ClusterID,
// in lowercase hex, so one cluster has one spelling.
func sanCluster(host string) (coreconsensus.ClusterID, error) {
	var cid coreconsensus.ClusterID
	b, err := hex.DecodeString(host)
	if err != nil || len(b) != len(cid) || hex.EncodeToString(b) != host {
		return cid, peerRejected("URI SAN cluster")
	}
	copy(cid[:], b)
	return cid, nil
}

// SAN returns the canonical URI SAN of a node certificate.
func SAN(cluster coreconsensus.ClusterID, slot string) *url.URL {
	return &url.URL{Scheme: sanScheme, Host: hex.EncodeToString(cluster[:]), Path: "/slot/" + slot}
}

// spkiOf is the pin of a certificate: the SHA-256 of its
// SubjectPublicKeyInfo, which survives a renewal over the same key.
func spkiOf(c *x509.Certificate) [32]byte { return sha256.Sum256(c.RawSubjectPublicKeyInfo) }

// SPKIOf returns the pin of a certificate: the SHA-256 of its
// SubjectPublicKeyInfo.
func SPKIOf(c *x509.Certificate) [32]byte { return spkiOf(c) }

type peerConn struct {
	conn  net.Conn
	queue chan []byte
	bytes int64
	mu    sync.Mutex
}

// TCPTransport is the shipped transport: TLS 1.3 with mutual
// authentication, a pinned SPKI per member, three connections per peer and
// direction, length-prefixed frames bounded before allocation.
type TCPTransport struct {
	cfg     TransportConfig
	id      *tlsid.Identity
	cluster coreconsensus.ClusterID
	slot    string
	ln      net.Listener

	mu        sync.Mutex
	members   map[coreconsensus.GroupID][]coreconsensus.MemberValue
	byID      map[coreconsensus.NodeID]coreconsensus.MemberValue
	bySPKI    map[[32]byte]coreconsensus.MemberValue
	out       map[outKey]*peerConn
	in        map[net.Conn][32]byte
	raw       map[net.Conn]struct{}
	admission *server.Admission
	resvMu    sync.Mutex
	resvSeq   atomic.Uint64
	// resvAddrs is the sorted address list last reserved, under mu: an
	// unchanged list is not reserved again (#531).
	resvAddrs   []string
	peerExpiry  map[coreconsensus.NodeID]time.Time
	deliver     func(coreconsensus.AuthenticatedPeerValue, []byte)
	offer       func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue
	install     func(coreconsensus.InstallParams) error
	join        func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code)
	joinR       func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)
	unreachable func(coreconsensus.NodeID)
	closed      bool
	wg          sync.WaitGroup
	done        chan struct{}
	// doneOnce closes done; Close reaches it once, under mu and behind closed.
	doneOnce sync.Once
}

type outKey struct {
	to   coreconsensus.NodeID
	lane byte
}

// validate applies the defaults and the refusals of the configuration.
func (c *TransportConfig) validate() error {
	clampInt(&c.MaxFrameBytes, 16<<20)
	clampInt(&c.MaxControlFrameBytes, 64<<10)
	clampDuration(&c.HandshakeTimeout, time.Second)
	clampInt64(&c.PeerQueueBytes, 64<<20)
	clampDuration(&c.MaxConnectionAge, 24*time.Hour)
	clampDuration(&c.WriteTimeout, 2*time.Second)
	clampDuration(&c.JoinTimeout, 10*time.Second)
	clampInt(&c.MaxUnauthenticatedPerIP, defaultUnauthenticatedPerIP)
	clampInt(&c.ReservedMemberSlots, defaultReservedMemberSlots)
	switch {
	case c.Roots == nil:
		return transportMisconfigured("Roots", "nil")
	case c.Certificate == nil && c.GetCertificate == nil:
		return transportMisconfigured("Certificate", "nil")
	case c.HandshakeTimeout > time.Second:
		return transportMisconfigured("HandshakeTimeout", "above 1 s")
	case c.MaxControlFrameBytes >= c.MaxFrameBytes:
		return transportMisconfigured("MaxControlFrameBytes", "not below MaxFrameBytes")
	case c.ListenAddress == "":
		return transportMisconfigured("ListenAddress", "empty")
	}
	return nil
}

// clampDuration replaces a non-positive duration with its default: a zero
// timeout is "unset", never "no timeout" (ADR 0031, clamp half).
func clampDuration(v *time.Duration, d time.Duration) {
	if *v <= 0 {
		*v = d
	}
}

// NewTCPTransport validates the configuration, reads the cluster and slot
// from the certificate, and listens (D4, R3-24). It launches the background
// accept goroutine, which Close stops with every connection goroutine. cfg
// is read, never written: the defaults are applied to a copy.
func NewTCPTransport(cfg *TransportConfig) (*TCPTransport, error) {
	c := *cfg
	if err := c.validate(); err != nil {
		return nil, err
	}
	return newTCPTransport(&c, nil)
}

// newTCPTransport builds the transport over id, or over the configuration's
// certificate source and roots when id is nil, then listens and launches
// the background accept goroutine, counted in t.wg, which ends when Close
// closes the listener.
func newTCPTransport(cfg *TransportConfig, id *tlsid.Identity) (*TCPTransport, error) {
	if id == nil {
		get := cfg.GetCertificate
		if get == nil {
			c := cfg.Certificate
			get = func() (*tls.Certificate, error) { return c, nil }
		}
		var err error
		if id, err = tlsid.NewIdentity(get, cfg.Roots); err != nil {
			return nil, transportMisconfigured("Certificate", err.Error())
		}
	}
	leaf, err := id.Leaf()
	if err != nil {
		return nil, transportMisconfigured("Certificate", "unparsable")
	}
	cid, slot, err := parseSAN(leaf)
	if err != nil {
		return nil, transportMisconfigured("Certificate", "URI SAN")
	}
	t := &TCPTransport{
		cfg: *cfg, id: id, cluster: cid, slot: slot,
		members: map[coreconsensus.GroupID][]coreconsensus.MemberValue{}, byID: map[coreconsensus.NodeID]coreconsensus.MemberValue{},
		bySPKI: map[[32]byte]coreconsensus.MemberValue{}, out: map[outKey]*peerConn{}, in: map[net.Conn][32]byte{}, raw: map[net.Conn]struct{}{}, admission: server.NewAdmission(server.AdmissionConfig{PerAddress: cfg.MaxUnauthenticatedPerIP, Reserved: cfg.ReservedMemberSlots}), peerExpiry: map[coreconsensus.NodeID]time.Time{},
		done: make(chan struct{}),
	}
	ln, err := listenTCP("tcp", cfg.ListenAddress)
	if err != nil {
		return nil, transportMisconfigured("ListenAddress", err.Error())
	}
	t.ln = ln
	t.wg.Add(1)
	go t.accept()
	return t, nil
}

// Addr returns the bound address.
func (t *TCPTransport) Addr() string { return t.ln.Addr().String() }

// Cluster returns the cluster the certificate names.
func (t *TCPTransport) Cluster() coreconsensus.ClusterID { return t.cluster }

// Slot returns the slot the certificate names.
func (t *TCPTransport) Slot() string { return t.slot }

// checkPeer is what the transport adds to the identity's verification, on
// every handshake: the one canonical URI SAN, of this cluster, and, when
// dialing a member, its pinned key (R2-21, R7-41, R8-07).
func (t *TCPTransport) checkPeer(pin *[32]byte) tlsid.Verify {
	return func(p tlsid.PeerIdentityValue) error {
		cid, _, err := parseSAN(p.Leaf)
		if err != nil {
			return err
		}
		if cid != t.cluster {
			return peerRejected("another cluster")
		}
		if pin != nil && p.SPKI != *pin {
			return peerRejected("key does not match the member's pin")
		}
		return nil
	}
}

// ---- inbound ----

// accept is the listener's background goroutine: it admits or refuses each
// connection before any TLS byte is read, and launches one serve goroutine
// per admitted connection, counted in t.wg, which ends with its connection
// (Close closes every one still raw). accept ends when the listener is
// closed, which is how Close stops it, and only then: any other failure —
// EMFILE or ENFILE when descriptors run out — is waited out on a growing
// backoff, as net/http's Serve does. Ending on it left the listener open,
// the kernel completing handshakes into its backlog, and nothing accepting
// them: the node sent but never received again, until a restart (#518).
func (t *TCPTransport) accept() {
	defer t.wg.Done()
	var delay time.Duration
	wait := time.NewTimer(acceptBackoffFirst)
	wait.Stop()
	defer wait.Stop()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			var goOn bool
			if delay, goOn = t.acceptFailed(err, delay, wait); !goOn {
				return
			}
			continue
		}
		delay = 0
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			// Accepted as the transport closed: dropped unread.
			abandon(c.Close())
			return
		}
		release, ok := t.admission.Admit(c.RemoteAddr())
		if !ok {
			// Refused before any TLS byte is read (R2-22, ADR 0174).
			t.mu.Unlock()
			abandon(c.Close())
			continue
		}
		t.raw[c] = struct{}{}
		t.wg.Add(1)
		t.mu.Unlock()
		go t.serve(c, release)
	}
}

// acceptFailed answers a failed Accept that followed a wait of delay: the
// end of the loop when the listener is closed, else the next wait, on the
// loop's one timer, cut short by the transport's close. It returns that
// wait, and whether the loop goes on.
func (t *TCPTransport) acceptFailed(err error, delay time.Duration, wait *time.Timer) (time.Duration, bool) {
	if errors.Is(err, net.ErrClosed) {
		return delay, false
	}
	delay = acceptBackoff(delay)
	wait.Reset(delay)
	select {
	case <-t.done:
		return delay, false
	case <-wait.C:
		return delay, true
	}
}

// acceptBackoff is the wait after a failed Accept that follows a wait of
// prev: acceptBackoffFirst first, doubled each time, at most
// acceptBackoffCeiling.
func acceptBackoff(prev time.Duration) time.Duration {
	if prev == 0 {
		return acceptBackoffFirst
	}
	return min(2*prev, acceptBackoffCeiling)
}

// serve runs one inbound connection: the handshake under its deadline,
// then the lane byte, then the lane itself. Only a member's pinned key on
// its own slot is served, except on the JOIN lane; the connection is
// closed and forgotten when serve returns, whatever ended it.
func (t *TCPTransport) serve(raw net.Conn, release func()) {
	defer t.wg.Done()
	defer func() {
		// The connection is over: whatever its Close says, nothing reads it.
		abandon(raw.Close())
		t.mu.Lock()
		delete(t.raw, raw)
		t.mu.Unlock()
	}()
	defer release()
	conn, pid, lane, ok := t.handshake(raw, release)
	if !ok {
		return
	}
	// The handshake's verification parsed this SAN already (checkPeer); a
	// second answer that differs is refused, never read as an empty slot.
	_, slot, err := parseSAN(pid.Leaf)
	if err != nil {
		return
	}
	t.serveAuthenticated(conn, &pid, lane, slot)
}

// handshake runs the TLS handshake of an inbound connection under its
// deadline, gives the admission slot back once it is over, and reads the
// lane byte; ok is false when any step failed and the connection must end.
func (t *TCPTransport) handshake(raw net.Conn, release func()) (conn *tls.Conn, pid tlsid.PeerIdentityValue, lane byte, ok bool) {
	conn = tls.Server(raw, t.id.ServerConfig(t.checkPeer(nil)))
	// No handshake runs without its deadline: a connection that cannot take
	// one is dropped, never left to hold its slot for as long as it likes.
	if err := conn.SetDeadline(time.Now().Add(t.cfg.HandshakeTimeout)); err != nil {
		return nil, pid, 0, false
	}
	err := conn.Handshake()
	// Authenticated or not, the connection no longer waits on its address.
	release()
	if err != nil {
		return nil, pid, 0, false
	}
	if pid, ok = tlsid.PeerOf(conn.ConnectionState()); !ok {
		return nil, pid, 0, false
	}
	var laneByte [1]byte
	if _, err := io.ReadFull(conn, laneByte[:]); err != nil {
		return nil, pid, 0, false
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, pid, 0, false
	}
	return conn, pid, laneByte[0], true
}

// serveAuthenticated serves a handshaken connection: registered for the
// membership's cuts while a member's, a JOIN for anyone, and a member's own
// lane only when the key and the slot are those the membership pins.
func (t *TCPTransport) serveAuthenticated(conn *tls.Conn, pid *tlsid.PeerIdentityValue, lane byte, slot string) {
	spki := pid.SPKI
	t.mu.Lock()
	m, member := t.bySPKI[spki]
	if member {
		t.in[conn] = spki
	}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.in, conn)
		t.mu.Unlock()
	}()
	if lane == laneJoin {
		t.serveJoin(conn, coreconsensus.AuthenticatedPeerValue{Slot: slot, SPKI: spki})
		return
	}
	if !member || m.Slot != slot {
		// Admission is keyed on the member's pinned SPKI and its slot
		// (R8-07): a valid certificate with another key is refused.
		return
	}
	peer := coreconsensus.AuthenticatedPeerValue{ID: m.ID, Incarnation: m.Incarnation, Slot: slot, SPKI: spki}
	t.serveMember(conn, peer, pid.Leaf, lane)
}

// serveMember serves a member's lane until its certificate expires, its
// connection reaches MaxConnectionAge, or it leaves the membership.
func (t *TCPTransport) serveMember(conn *tls.Conn, peer coreconsensus.AuthenticatedPeerValue, leaf *x509.Certificate, lane byte) {
	t.mu.Lock()
	t.peerExpiry[peer.ID] = leaf.NotAfter
	t.mu.Unlock()
	deadline := time.Now().Add(t.cfg.MaxConnectionAge)
	if leaf.NotAfter.Before(deadline) {
		deadline = leaf.NotAfter
	}
	// The read deadline is what ends the connection when the peer's
	// certificate expires: a connection that cannot take it is dropped.
	if err := conn.SetReadDeadline(deadline); err != nil {
		return
	}
	switch lane {
	case laneControl, laneData:
		limit := t.cfg.MaxFrameBytes
		if lane == laneControl {
			limit = t.cfg.MaxControlFrameBytes
		}
		t.readFrames(conn, peer, limit)
	case laneSnapshot:
		t.serveSnapshot(conn, peer)
	default:
		// An unknown lane is served nothing: the connection ends, as it
		// always has.
	}
}

// readFrames delivers a member's length-prefixed frames, each bound by
// limit before anything is allocated, and stops at the first malformed
// frame, read error, or once the peer is no longer a member. One buffer is
// reused for the connection's life: deliver copies what it keeps.
func (t *TCPTransport) readFrames(c net.Conn, peer coreconsensus.AuthenticatedPeerValue, limit int) {
	r := bufio.NewReaderSize(c, connBufferBytes)
	var buf []byte
	for {
		frame, ok := readFrame(r, buf, limit)
		if !ok {
			return
		}
		buf = frame
		d, still := t.deliverTo(peer.SPKI)
		if !still {
			return
		}
		if d != nil {
			d(peer, frame)
		}
	}
}

// readFrame reads one frame into buf, grown only when the announced length
// needs it; ok is false on a read error or a length outside
// [minFrameBody, limit-4].
func readFrame(r io.Reader, buf []byte, limit int) ([]byte, bool) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return buf, false
	}
	n := int(binary.LittleEndian.Uint32(h[:]))
	// The announced length is checked before anything is allocated.
	if n < minFrameBody || n+4 > limit {
		return buf, false
	}
	if cap(buf) < n+4 {
		buf = make([]byte, n+4)
	}
	buf = buf[:n+4]
	copy(buf, h[:])
	if _, err := io.ReadFull(r, buf[4:]); err != nil {
		return buf, false
	}
	return buf, true
}

// deliverTo is the bound deliver callback and whether the key is still a
// member's: a reader whose peer left stops at its next frame.
func (t *TCPTransport) deliverTo(spki [32]byte) (func(coreconsensus.AuthenticatedPeerValue, []byte), bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, still := t.bySPKI[spki]
	return t.deliver, still
}

// ---- outbound ----

// dial opens an outbound connection of one lane: the handshake verifies
// the chain and, when pin is set, the member's key, then the lane byte is
// written. A connection that fails any step is closed and not returned.
func (t *TCPTransport) dial(ctx context.Context, addr string, lane byte, pin *[32]byte) (*tls.Conn, error) {
	return t.dialVerified(ctx, addr, lane, t.checkPeer(pin))
}

// dialAny dials a peer that must present one of pins.
func (t *TCPTransport) dialAny(ctx context.Context, addr string, lane byte, pins [][32]byte) (*tls.Conn, error) {
	check := t.checkPeer(nil)
	return t.dialVerified(ctx, addr, lane, func(p tlsid.PeerIdentityValue) error {
		if err := check(p); err != nil {
			return err
		}
		if !slices.Contains(pins, p.SPKI) {
			return peerRejected("key is not one the joiner pins")
		}
		return nil
	})
}

// dialVerified dials addr, runs the identity's verification then verify,
// and writes the lane byte.
func (t *TCPTransport) dialVerified(ctx context.Context, addr string, lane byte, verify tlsid.Verify) (*tls.Conn, error) {
	d := net.Dialer{Timeout: t.cfg.HandshakeTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// The identity verifies the chain, roots and extended key usage first,
	// then the transport's SAN and pin check runs (R2-21, ADR 0174 D1).
	c := tls.Client(raw, t.id.ClientConfig(verify))
	// A connection that fails a step is dropped unused: the step's error is
	// the one returned, and its Close has no reader.
	if err := c.SetDeadline(time.Now().Add(t.cfg.HandshakeTimeout)); err != nil {
		abandon(raw.Close())
		return nil, err
	}
	if err := c.HandshakeContext(ctx); err != nil {
		abandon(raw.Close())
		return nil, err
	}
	if _, err := c.Write([]byte{lane}); err != nil {
		abandon(c.Close())
		return nil, err
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		abandon(c.Close())
		return nil, err
	}
	return c, nil
}

// address is where to dial a member and the key to pin it to: its
// committed address, else its seed. A member without a pin is never
// dialled, since nothing would tell it from an impostor.
func (t *TCPTransport) address(to coreconsensus.NodeID) (string, [32]byte, bool) {
	var noPin [32]byte
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.byID[to]
	if !ok || m.SPKI == noPin {
		return "", noPin, false
	}
	addr := m.Address
	if a, ok := t.cfg.Seeds[to]; ok && addr == "" {
		addr = a
	}
	return addr, m.SPKI, addr != ""
}

// Send implements Transport: it never blocks, and drops when the peer's
// queue is full (D4). The first frame to a peer and lane launches that
// lane's background sender goroutine, which ends when Close is called.
func (t *TCPTransport) Send(to coreconsensus.NodeID, frame []byte) {
	lane := laneData
	if l, err := wire.Lane(frame); err == nil && l == coreconsensus.LaneControl {
		lane = laneControl
	}
	k := outKey{to, lane}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	pc := t.out[k]
	if pc == nil {
		pc = &peerConn{queue: make(chan []byte, senderQueueFrames)}
		t.out[k] = pc
		t.wg.Add(1)
		go t.sender(k, pc)
	}
	t.mu.Unlock()
	pc.mu.Lock()
	if pc.bytes+int64(len(frame)) > t.cfg.PeerQueueBytes {
		pc.mu.Unlock()
		return
	}
	pc.bytes += int64(len(frame))
	pc.mu.Unlock()
	select {
	case pc.queue <- frame:
	default:
		pc.mu.Lock()
		pc.bytes -= int64(len(frame))
		pc.mu.Unlock()
	}
}

// sender writes every frame queued for one peer and lane in one write; a
// write that misses its deadline closes the connection and reports the
// peer unreachable (D4, R2-06). It is the background goroutine Send
// launches per peer and lane, counted in t.wg, and ends when Close closes
// done; its connection is closed on the way out.
func (t *TCPTransport) sender(k outKey, pc *peerConn) {
	defer t.wg.Done()
	var link senderLink
	defer link.close()
	var batch [][]byte
	for {
		var f []byte
		select {
		case <-t.done:
			return
		case f = <-pc.queue:
		}
		batch = takeBatch(append(batch[:0], f), pc)
		t.sendBatch(k, &link, batch)
		// The frames are written or dropped: the reused batch keeps no
		// reference to them.
		clear(batch)
	}
}

// senderLink is a sender's connection to its peer and lane, with what it
// needs to retire it by age and to pace redials after a failure.
type senderLink struct {
	conn    *tls.Conn
	w       *bufio.Writer
	created time.Time
	backoff time.Time
}

// close drops the link's connection: the sender is stopping, or the
// connection is retired or broken, and nothing is left to write on it.
func (l *senderLink) close() {
	if l.conn != nil {
		abandon(l.conn.Close())
		l.conn = nil
	}
}

// takeBatch adds to batch every frame already queued, up to maxSenderBatch,
// and releases their bytes from the peer's queue budget.
func takeBatch(batch [][]byte, pc *peerConn) [][]byte {
	for len(batch) < maxSenderBatch {
		g, ok := queuedFrame(pc)
		if !ok {
			break
		}
		batch = append(batch, g)
	}
	size := 0
	for _, b := range batch {
		size += len(b)
	}
	pc.mu.Lock()
	pc.bytes -= int64(size)
	pc.mu.Unlock()
	return batch
}

// queuedFrame takes a frame already queued, without waiting. The queue is
// never closed, so a closed one reads as empty.
func queuedFrame(pc *peerConn) ([]byte, bool) {
	select {
	case g, ok := <-pc.queue:
		return g, ok
	default:
		return nil, false
	}
}

// sendBatch writes one batch on the link, dialling first when there is no
// connection; a batch with no connection to go to is dropped, and Raft
// retransmits what matters. A connection that was already open and fails
// the write is dialled again once and the batch written on the new one: it
// died while the link was quiet — its peer reset it, or the kernel aborted
// it under the user timeout (boundUnacknowledged) — and the batch, an
// election's vote among others, would otherwise wait for Raft's next round.
func (t *TCPTransport) sendBatch(k outKey, link *senderLink, batch [][]byte) {
	if link.conn != nil && time.Since(link.created) > t.cfg.MaxConnectionAge {
		// Retired by age, every frame of it flushed: the next batch
		// dials a fresh one.
		link.close()
	}
	reused := link.conn != nil
	if !reused && !t.redial(k, link) {
		return
	}
	err := t.writeBatch(link, batch)
	if err != nil && reused {
		link.close()
		if !t.redial(k, link) {
			return
		}
		err = t.writeBatch(link, batch)
	}
	if err != nil {
		// The failure is reported as the peer unreachable; the socket
		// itself is beyond use.
		link.close()
		t.reportUnreachable(k.to)
	}
}

// writeBatch writes the batch on the link's connection and flushes it,
// under one write deadline.
func (t *TCPTransport) writeBatch(link *senderLink, batch [][]byte) error {
	// A write without its deadline could block this sender for good,
	// so a connection that refuses one fails like a write that missed
	// it (D4, R2-06).
	err := link.conn.SetWriteDeadline(time.Now().Add(t.cfg.WriteTimeout))
	for _, b := range batch {
		if err != nil {
			return err
		}
		_, err = link.w.Write(b)
	}
	if err != nil {
		return err
	}
	return link.w.Flush()
}

// redial connects the link, unless a failed dial is still backing off or
// the peer has no address; a failed dial starts the backoff and reports
// the peer unreachable. It reports whether the link is connected. Bytes
// left unacknowledged on the new connection for WriteTimeout abort it, as
// a write that blocks that long does; a connection that refuses the bound
// fails like a failed dial.
func (t *TCPTransport) redial(k outKey, link *senderLink) bool {
	if time.Now().Before(link.backoff) {
		return false // dropped: Raft retransmits what matters
	}
	addr, pin, ok := t.address(k.to)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.HandshakeTimeout)
	defer cancel()
	c, err := t.dial(ctx, addr, k.lane, &pin)
	if err == nil {
		if err = boundUnacknowledged(c.NetConn(), t.cfg.WriteTimeout); err != nil {
			abandon(c.Close())
		}
	}
	if err != nil {
		link.backoff = time.Now().Add(redialBackoff)
		t.reportUnreachable(k.to)
		return false
	}
	link.conn, link.w, link.created = c, bufio.NewWriterSize(c, connBufferBytes), time.Now()
	return true
}

// reportUnreachable tells the engine, when it asked to know, that a peer
// could not be reached.
func (t *TCPTransport) reportUnreachable(id coreconsensus.NodeID) {
	t.mu.Lock()
	f := t.unreachable
	t.mu.Unlock()
	if f != nil {
		f(id)
	}
}

// Unreachable implements UnreachableReporter.
func (t *TCPTransport) Unreachable(f func(coreconsensus.NodeID)) {
	t.mu.Lock()
	t.unreachable = f
	t.mu.Unlock()
}

// Bind implements Transport.
func (t *TCPTransport) Bind(_ coreconsensus.GroupID,
	deliver func(coreconsensus.AuthenticatedPeerValue, []byte),
	offer func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue,
	install func(coreconsensus.InstallParams) error,
	join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code),
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deliver, t.offer, t.install, t.join = deliver, offer, install, join
	return nil
}

// SetMembership implements Transport: the admitted set is the union over
// the groups; a member that left has its connections cut and its
// handshakes refused from then on (D4).
func (t *TCPTransport) SetMembership(group coreconsensus.GroupID, admitted []coreconsensus.MemberValue) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.members[group] = slices.Clone(admitted)
	byID, bySPKI := indexMembers(t.members)
	t.byID, t.bySPKI = byID, bySPKI
	t.reserveAddresses(byID)
	for id := range t.peerExpiry {
		if _, ok := byID[id]; !ok {
			// A member that left has no certificate to expire here: its
			// last one kept being published, crossed the alert threshold
			// and went negative (#532).
			delete(t.peerExpiry, id)
		}
	}
	for c, spki := range t.in {
		if _, ok := bySPKI[spki]; !ok {
			// A member that left is cut (D4); its reader ends on the closed
			// socket, which is all this Close is for.
			abandon(c.Close())
		}
	}
}

// indexMembers is the union of every group's admitted members, by id and
// by pinned key; a member without a pin is reachable by id only, so it can
// be dialled for nothing and admitted on nothing, and a key two members
// share admits neither.
func indexMembers(groups map[coreconsensus.GroupID][]coreconsensus.MemberValue) (map[coreconsensus.NodeID]coreconsensus.MemberValue, map[[32]byte]coreconsensus.MemberValue) {
	var noPin [32]byte
	byID := map[coreconsensus.NodeID]coreconsensus.MemberValue{}
	bySPKI := map[[32]byte]coreconsensus.MemberValue{}
	shared := map[[32]byte]bool{}
	for _, ms := range groups {
		for _, m := range ms {
			byID[m.ID] = m
			if m.SPKI == noPin {
				continue
			}
			if o, ok := bySPKI[m.SPKI]; ok && o.ID != m.ID {
				// Two members under one key: a connection could speak for
				// either, so the key admits neither (review M5).
				shared[m.SPKI] = true
			}
			bySPKI[m.SPKI] = m
		}
	}
	for k := range shared {
		delete(bySPKI, k)
	}
	return byID, bySPKI
}

// reserveAddresses gives the members' addresses their reserved admission
// budget; one that does not resolve gets none (ADR 0174 D2). Resolving may
// block, and SetMembership runs on the engine's event loop, so it runs on a
// background goroutine counted in t.wg, which ends once its set is applied
// or found stale: only the newest set lands. The caller holds t.mu.
func (t *TCPTransport) reserveAddresses(byID map[coreconsensus.NodeID]coreconsensus.MemberValue) {
	addrs := make([]string, 0, len(byID))
	for _, m := range byID {
		if m.Address != "" {
			addrs = append(addrs, m.Address)
		}
	}
	slices.Sort(addrs)
	if t.resvAddrs != nil && slices.Equal(addrs, t.resvAddrs) {
		return
	}
	t.resvAddrs = addrs
	seq := t.resvSeq.Add(1)
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.resvMu.Lock()
		defer t.resvMu.Unlock()
		if t.resvSeq.Load() == seq {
			t.admission.SetReserved(addrs)
		}
	}()
}

// Close stops the transport and joins its goroutines.
func (t *TCPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.doneOnce.Do(func() { close(t.done) })
	for c := range t.raw {
		// Unblocks the connection's reader so wg.Wait returns; the transport
		// is closing and reads nothing more from it.
		abandon(c.Close())
	}
	t.mu.Unlock()
	err := t.ln.Close()
	t.wg.Wait()
	return err
}

// ---- snapshots: offer, accept, then the stream (R7-08) ----

// writeChunk writes one length-prefixed chunk of the snapshot and JOIN
// lanes.
func writeChunk(w io.Writer, b []byte) error {
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readChunk reads one length-prefixed chunk, refusing a length above limit
// before allocating it.
func readChunk(r io.Reader, limit int) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	// Compared before the conversion: on a 32-bit platform a length of
	// 2^31 or more would turn negative, pass the bound and panic make (#422).
	ln := binary.LittleEndian.Uint32(h[:])
	if limit < 0 || uint64(ln) > uint64(limit) {
		return nil, origin(coreconsensus.FrameInvalid, errs.Int64("length", int64(ln)))
	}
	b := make([]byte, int(ln))
	_, err := io.ReadFull(r, b)
	return b, err
}

// encodeOffer is the wire form of a snapshot offer: its term, then the
// metadata.
func encodeOffer(o coreconsensus.InstallOfferValue) []byte {
	b := binary.LittleEndian.AppendUint64(nil, o.Term)
	return wire.AppendSnapshotMeta(b, &o.Meta)
}

// decodeOffer parses an offer; its snapshot id is the metadata's, never a
// second field that could disagree with it.
func decodeOffer(b []byte) (coreconsensus.InstallOfferValue, error) {
	if len(b) < 8 {
		return coreconsensus.InstallOfferValue{}, origin(coreconsensus.FrameInvalid)
	}
	o := coreconsensus.InstallOfferValue{Term: binary.LittleEndian.Uint64(b)}
	if err := wire.ParseSnapshotMeta(b[8:], &o.Meta); err != nil {
		return o, err
	}
	o.SnapshotID = o.Meta.ID
	return o, nil
}

// Stream implements Transport.
func (t *TCPTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	addr, pin, ok := t.address(to)
	if !ok {
		return peerRejected("no address")
	}
	c, err := t.dial(ctx, addr, laneSnapshot, &pin)
	if err != nil {
		return err
	}
	// The stream's verdict is the receiver's install result, read before
	// this Close; a Close of a cancelled stream unblocks its I/O and says
	// nothing the error returned does not.
	defer func() { abandon(c.Close()) }()
	stop := context.AfterFunc(ctx, func() { abandon(c.Close()) })
	defer stop()
	if err := writeChunk(c, encodeOffer(offer)); err != nil {
		return err
	}
	acc, err := readChunk(c, 64)
	if err != nil || len(acc) != 13 {
		return origin(coreconsensus.FrameInvalid, errs.String("what", "install accept"))
	}
	off := int64(binary.LittleEndian.Uint64(acc))
	code := errs.Code(binary.LittleEndian.Uint32(acc[8:]))
	if acc[12] != 0 {
		return nil // held: an idempotent success
	}
	if code != 0 {
		return origin(sentinelOf(code))
	}
	if err := streamFrom(c, open, off); err != nil {
		return err
	}
	return installResult(c)
}

// streamFrom copies the snapshot from the accepted offset and closes the
// write half, so the receiver sees the stream end. A Close failure of the
// source is reported only when the copy itself succeeded.
func streamFrom(c closeWriter, open func(int64) (io.ReadCloser, error), off int64) error {
	rc, err := open(off)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, rc)
	if cerr := rc.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return c.CloseWrite()
}

// closeWriter is the end of a snapshot stream the sender writes and then
// half-closes, so the receiver sees the stream end and can still answer.
type closeWriter interface {
	io.Writer
	CloseWrite() error
}

// installResult reads the receiver's verdict on the install, the last
// chunk of the stream.
func installResult(c *tls.Conn) error {
	res, err := readChunk(c, 8)
	if err != nil || len(res) != 4 {
		return origin(coreconsensus.FrameInvalid, errs.String("what", "install result"))
	}
	if code := errs.Code(binary.LittleEndian.Uint32(res)); code != 0 {
		return origin(sentinelOf(code))
	}
	return nil
}

// serveSnapshot is the receiving end of a snapshot stream: it reads the
// offer, answers with the engine's acceptance, and when accepted hands the
// stream to install, then writes back the install's verdict.
func (t *TCPTransport) serveSnapshot(c *tls.Conn, peer coreconsensus.AuthenticatedPeerValue) {
	b, err := readChunk(c, t.cfg.MaxControlFrameBytes)
	if err != nil {
		return
	}
	o, err := decodeOffer(b)
	if err != nil {
		return
	}
	t.mu.Lock()
	of, in := t.offer, t.install
	t.mu.Unlock()
	if of == nil || in == nil {
		return
	}
	acc := of(peer, o)
	reply := binary.LittleEndian.AppendUint64(nil, uint64(acc.Offset))
	reply = binary.LittleEndian.AppendUint32(reply, uint32(acc.Refused))
	reply = append(reply, boolByte(acc.Held))
	if err := writeChunk(c, reply); err != nil || acc.Refused != 0 || acc.Held {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ierr := in(coreconsensus.InstallParams{
		Ctx: ctx, From: peer, Offer: o, Offset: acc.Offset,
		Body: io.LimitReader(c, o.Meta.EncodedSize-acc.Offset),
	})
	var code errs.Code
	if ierr != nil {
		code, _ = errs.CodeOf(ierr)
		if code == 0 {
			code = coreconsensus.CodeSnapshotCorrupt
		}
	}
	// The last write of the stream: a sender that does not read it sees the
	// connection end and treats the install as failed, which it retries.
	abandon(writeChunk(c, binary.LittleEndian.AppendUint32(nil, uint32(code))))
}

// ---- pre-admission JOIN (D4, R7-42, R8-09) ----

// encodeJoin is the wire form of a JOIN: cluster, id, incarnation, the
// slot with its one-byte length, then the address to the end.
func encodeJoin(r coreconsensus.JoinRequestValue) []byte {
	b := slices.Clone(r.Cluster[:])
	b = binary.LittleEndian.AppendUint64(b, uint64(r.ID))
	b = append(b, r.Incarnation[:]...)
	b = append(b, byte(len(r.Slot)))
	b = append(b, r.Slot...)
	return append(b, r.Address...)
}

// decodeJoin parses a JOIN, refusing one too short for its fixed part or
// whose slot overruns it.
func decodeJoin(b []byte) (coreconsensus.JoinRequestValue, bool) {
	var r coreconsensus.JoinRequestValue
	if len(b) < 16+8+16+1 {
		return r, false
	}
	copy(r.Cluster[:], b)
	r.ID = coreconsensus.NodeID(binary.LittleEndian.Uint64(b[16:]))
	copy(r.Incarnation[:], b[24:])
	n := int(b[40])
	if 41+n > len(b) {
		return r, false
	}
	r.Slot = string(b[41 : 41+n])
	r.Address = string(b[41+n:])
	return r, true
}

// serveJoin answers the one frame a non-member may send. The slot comes
// from the certificate, the SPKI from the handshake; join runs off the event
// loop under a deadline (R9-06).
func (t *TCPTransport) serveJoin(c net.Conn, peer coreconsensus.AuthenticatedPeerValue) {
	// A JOIN is answered under its deadline or not at all (R9-06).
	if err := c.SetDeadline(time.Now().Add(t.cfg.JoinTimeout)); err != nil {
		return
	}
	b, err := readChunk(c, 512)
	if err != nil {
		return
	}
	req, ok := decodeJoin(b)
	reply := func(code errs.Code, pos coreconsensus.PositionValue, reason string) {
		out := binary.LittleEndian.AppendUint32(nil, uint32(code))
		out = binary.LittleEndian.AppendUint64(out, pos.Term)
		out = binary.LittleEndian.AppendUint64(out, pos.Index)
		if code != 0 {
			// JOIN_REFUSED carries its reason, sanitized and bounded here
			// too, whatever the callback returned.
			r := sanitizeReason(reason)
			out = binary.LittleEndian.AppendUint16(out, uint16(len(r)))
			out = append(out, r...)
		}
		if code == 0 {
			// The admitted members, so the joiner's transport admits the
			// leader that will feed it before its own log names anyone.
			t.mu.Lock()
			m := coreconsensus.MembershipValue{Members: slices.Collect(maps.Values(t.byID))}
			t.mu.Unlock()
			out = wire.AppendMembership(out, &m)
		}
		// The answer is the last write of a pre-admission connection: a
		// joiner that does not read it fails its JOIN and tries the next
		// address.
		abandon(writeChunk(c, out))
	}
	if !ok {
		reply(coreconsensus.CodeJoinRefused, coreconsensus.PositionValue{}, "malformed JOIN")
		return
	}
	if req.Slot != peer.Slot {
		reply(coreconsensus.CodeJoinRefused, coreconsensus.PositionValue{}, "the slot differs from the certificate's")
		return
	}
	req.SPKI = peer.SPKI
	t.mu.Lock()
	j, jr := t.join, t.joinR
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.JoinTimeout)
	defer cancel()
	switch {
	case jr != nil:
		pos, code, reason := jr(ctx, peer, req)
		reply(code, pos, reason)
	case j != nil:
		pos, code := j(ctx, peer, req)
		reply(code, pos, "")
	default:
		reply(coreconsensus.CodeJoinRefused, coreconsensus.PositionValue{}, "")
	}
}

// Join implements Joiner: the JOIN goes to each address in turn until one
// member answers JOIN_ACK with the position its AddLearner was appended at.
func (t *TCPTransport) Join(ctx context.Context, cfg coreconsensus.JoinConfig) (coreconsensus.PositionValue, error) {
	req := coreconsensus.JoinRequestValue{
		Cluster: cfg.Stamp.Log.Cluster, ID: cfg.Stamp.ID, Incarnation: cfg.Stamp.Incarnation,
		Slot: t.slot, Address: cfg.Address,
	}
	if req.Cluster != t.cluster {
		return coreconsensus.PositionValue{}, origin(coreconsensus.ClusterMismatch)
	}
	if len(cfg.Pins) == 0 {
		// The answer installs the peers the joiner admits: it is taken only
		// from a key the operator vouched for (review M4).
		return coreconsensus.PositionValue{}, transportMisconfigured("JoinConfig.Pins",
			"empty: pass the members' SPKIs, from the founding member list (id:inc:slot:spki:addr) or a running member's Membership()")
	}
	last := origin(coreconsensus.JoinRefused, errs.String("why", "no address answered"))
	var refused error
	for _, addr := range cfg.Addresses {
		pos, answered, err := t.joinOne(ctx, addr, req, cfg.Pins)
		switch {
		case err == nil:
			return pos, nil
		case answered:
			// A refusal beats an unreachable address: it says why.
			refused = err
		default:
			last = err
		}
	}
	if refused != nil {
		return coreconsensus.PositionValue{}, refused
	}
	return coreconsensus.PositionValue{}, last
}

// joinOne sends the JOIN to one address, to a peer presenting one of pins,
// and installs the membership it answers once that membership lists the
// peer under its key. refused is set when the peer answered with a refusal.
func (t *TCPTransport) joinOne(ctx context.Context, addr string, req coreconsensus.JoinRequestValue, pins [][32]byte) (pos coreconsensus.PositionValue, refused bool, err error) {
	c, derr := t.dialAny(ctx, addr, laneJoin, pins)
	if derr != nil {
		return coreconsensus.PositionValue{}, false, derr
	}
	// The answer is read, or its failure is: the socket has served.
	defer func() { abandon(c.Close()) }()
	peer, verified := tlsid.PeerOf(c.ConnectionState())
	if !verified {
		return coreconsensus.PositionValue{}, false, peerRejected("no verified peer")
	}
	// A JOIN is answered under its deadline or not at all (R9-06).
	if err := c.SetDeadline(time.Now().Add(t.cfg.JoinTimeout)); err != nil {
		return coreconsensus.PositionValue{}, false, err
	}
	if err := writeChunk(c, encodeJoin(req)); err != nil {
		return coreconsensus.PositionValue{}, false, err
	}
	b, rerr := readChunk(c, t.cfg.MaxControlFrameBytes)
	if rerr != nil || len(b) < 20 {
		return coreconsensus.PositionValue{}, false, origin(coreconsensus.FrameInvalid, errs.String("what", "join answer"))
	}
	if code := errs.Code(binary.LittleEndian.Uint32(b)); code != 0 {
		reason, _ := joinAnswerReason(b[20:])
		return coreconsensus.PositionValue{}, true, joinRefused(code, reason)
	}
	m, perr := wire.ParseMembership(b[20:])
	if perr != nil {
		return coreconsensus.PositionValue{}, false, perr
	}
	if !joinAnswerListsPeer(m, peer.SPKI) {
		return coreconsensus.PositionValue{}, false, peerRejected("the answer does not list its own sender")
	}
	t.SetMembership(1, m.Members)
	return coreconsensus.PositionValue{Term: binary.LittleEndian.Uint64(b[4:]), Index: binary.LittleEndian.Uint64(b[12:])}, false, nil
}

// joinAnswerListsPeer reports whether a JOIN answer's membership lists the
// peer that sent it under the key it presented: an answer that does not is
// not the cluster's (review M4).
func joinAnswerListsPeer(m coreconsensus.MembershipValue, spki [32]byte) bool {
	return slices.ContainsFunc(m.Members, func(x coreconsensus.MemberValue) bool { return x.SPKI == spki })
}

// joinAnswerReason reads the reason after a JOIN_REFUSED's fixed part. ok
// is false when the answer carries none — an older member's, or one cut
// short — which the refusal then reports without a reason.
func joinAnswerReason(b []byte) (reason string, ok bool) {
	if len(b) < 2 {
		return "", false
	}
	n := int(binary.LittleEndian.Uint16(b))
	if 2+n > len(b) {
		return "", false
	}
	return string(b[2 : 2+n]), true
}

// BindJoinReason implements JoinReasonBinder: the JOIN callback that also
// says why it refused.
func (t *TCPTransport) BindJoinReason(_ coreconsensus.GroupID, join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)) {
	t.mu.Lock()
	t.joinR = join
	t.mu.Unlock()
}

// RefusedBeforeHandshake counts the connections refused by the
// pre-handshake admission.
func (t *TCPTransport) RefusedBeforeHandshake() int64 { return t.admission.Refused() }

// CertExpiry returns when the transport's own certificate expires.
func (t *TCPTransport) CertExpiry() time.Time {
	leaf, err := t.id.Leaf()
	if err != nil {
		return time.Time{}
	}
	return leaf.NotAfter
}

// PeerCertExpiry returns when each admitted peer's last-seen certificate
// expires.
func (t *TCPTransport) PeerCertExpiry() map[coreconsensus.NodeID]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.peerExpiry)
}

// ReloadFailures counts the refused reloads of a PEM identity, 0 without one.
func (t *TCPTransport) ReloadFailures() int { return int(t.id.ReloadFailures()) }

// maxFrameBytes is the largest frame this transport reads: the engine
// refuses a batch bound above it.
func (t *TCPTransport) maxFrameBytes() int { return t.cfg.MaxFrameBytes }
