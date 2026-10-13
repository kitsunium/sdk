package consensus

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// TransportConfig builds the shipped mTLS transport (D4, D14).
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

const (
	laneControl  byte = 0
	laneData     byte = 1
	laneSnapshot byte = 2
	laneJoin     byte = 3

	sanScheme = "kitsunium-consensus"
)

func transportMisconfigured(field, why string) error {
	return origin(coreconsensus.TransportMisconfigured, errs.String("field", field), errs.String("why", why))
}

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
	b, err := hex.DecodeString(u.Host)
	if err != nil || len(b) != 16 || hex.EncodeToString(b) != u.Host {
		return cid, "", peerRejected("URI SAN cluster")
	}
	copy(cid[:], b)
	slot, ok := strings.CutPrefix(u.Path, "/slot/")
	if !ok || !validSlot(slot) || u.String() != sanScheme+"://"+u.Host+"/slot/"+slot {
		return cid, "", peerRejected("URI SAN slot")
	}
	return cid, slot, nil
}

// SAN returns the canonical URI SAN of a node certificate.
func SAN(cluster coreconsensus.ClusterID, slot string) *url.URL {
	return &url.URL{Scheme: sanScheme, Host: cluster.String(), Path: "/slot/" + slot}
}

func spkiOf(c *x509.Certificate) [32]byte { return sha256.Sum256(c.RawSubjectPublicKeyInfo) }

// SPKIOf returns the pin of a certificate: the SHA-256 of its
// SubjectPublicKeyInfo.
func SPKIOf(c *x509.Certificate) [32]byte { return spkiOf(c) }

type peerConn struct {
	conn    net.Conn
	queue   chan []byte
	bytes   int64
	mu      sync.Mutex
	closed  bool
	created time.Time
}

// TCPTransport is the shipped transport: TLS 1.3 with mutual
// authentication, a pinned SPKI per member, three connections per peer and
// direction, length-prefixed frames bounded before allocation.
type TCPTransport struct {
	cfg     TransportConfig
	cluster coreconsensus.ClusterID
	slot    string
	ln      net.Listener

	mu          sync.Mutex
	members     map[coreconsensus.GroupID][]coreconsensus.Member
	byID        map[coreconsensus.NodeID]coreconsensus.Member
	bySPKI      map[[32]byte]coreconsensus.Member
	out         map[outKey]*peerConn
	in          map[net.Conn][32]byte
	raw         map[net.Conn]struct{}
	pending     map[string]int
	memberIPs   map[string]bool
	refusedPre  int64
	peerExpiry  map[coreconsensus.NodeID]time.Time
	reloads     func() int
	deliver     func(coreconsensus.AuthenticatedPeer, []byte)
	offer       func(coreconsensus.AuthenticatedPeer, coreconsensus.InstallOffer) coreconsensus.InstallAccept
	install     func(coreconsensus.InstallRequest) error
	join        func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code)
	unreachable func(coreconsensus.NodeID)
	closed      bool
	wg          sync.WaitGroup
	done        chan struct{}
}

type outKey struct {
	to   coreconsensus.NodeID
	lane byte
}

// NewTCPTransport validates the configuration, reads the cluster and slot
// from the certificate, and listens (D4, R3-24).
func NewTCPTransport(cfg TransportConfig) (*TCPTransport, error) {
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 16 << 20
	}
	if cfg.MaxControlFrameBytes <= 0 {
		cfg.MaxControlFrameBytes = 64 << 10
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = time.Second
	}
	if cfg.PeerQueueBytes <= 0 {
		cfg.PeerQueueBytes = 64 << 20
	}
	if cfg.MaxConnectionAge <= 0 {
		cfg.MaxConnectionAge = 24 * time.Hour
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 2 * time.Second
	}
	if cfg.JoinTimeout <= 0 {
		cfg.JoinTimeout = 10 * time.Second
	}
	if cfg.MaxUnauthenticatedPerIP <= 0 {
		cfg.MaxUnauthenticatedPerIP = 4
	}
	if cfg.ReservedMemberSlots <= 0 {
		cfg.ReservedMemberSlots = 8
	}
	switch {
	case cfg.Roots == nil:
		return nil, transportMisconfigured("Roots", "nil")
	case cfg.Certificate == nil && cfg.GetCertificate == nil:
		return nil, transportMisconfigured("Certificate", "nil")
	case cfg.HandshakeTimeout > time.Second:
		return nil, transportMisconfigured("HandshakeTimeout", "above 1 s")
	case cfg.MaxControlFrameBytes >= cfg.MaxFrameBytes:
		return nil, transportMisconfigured("MaxControlFrameBytes", "not below MaxFrameBytes")
	case cfg.ListenAddress == "":
		return nil, transportMisconfigured("ListenAddress", "empty")
	}
	cert, err := (&TCPTransport{cfg: cfg}).certificate()
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, transportMisconfigured("Certificate", "unparsable")
	}
	cid, slot, err := parseSAN(leaf)
	if err != nil {
		return nil, transportMisconfigured("Certificate", "URI SAN")
	}
	t := &TCPTransport{cfg: cfg, cluster: cid, slot: slot,
		members: map[coreconsensus.GroupID][]coreconsensus.Member{}, byID: map[coreconsensus.NodeID]coreconsensus.Member{},
		bySPKI: map[[32]byte]coreconsensus.Member{}, out: map[outKey]*peerConn{}, in: map[net.Conn][32]byte{}, raw: map[net.Conn]struct{}{}, pending: map[string]int{}, memberIPs: map[string]bool{}, peerExpiry: map[coreconsensus.NodeID]time.Time{},
		done: make(chan struct{})}
	ln, err := net.Listen("tcp", cfg.ListenAddress)
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

func (t *TCPTransport) certificate() (*tls.Certificate, error) {
	if t.cfg.GetCertificate != nil {
		c, err := t.cfg.GetCertificate()
		if err != nil || c == nil || len(c.Certificate) == 0 {
			return nil, transportMisconfigured("GetCertificate", "no certificate")
		}
		return c, nil
	}
	return t.cfg.Certificate, nil
}

// verifyChain runs the full x509 verification, roots and extended key
// usage, before any SAN is read (R2-21).
func (t *TCPTransport) verifyChain(cs tls.ConnectionState, usage x509.ExtKeyUsage) (*x509.Certificate, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, peerRejected("no certificate")
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: t.cfg.Roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return nil, peerRejected("chain: " + err.Error())
	}
	cid, _, err := parseSAN(leaf)
	if err != nil {
		return nil, err
	}
	if cid != t.cluster {
		return nil, peerRejected("another cluster")
	}
	return leaf, nil
}

func (t *TCPTransport) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Session tickets are off, so a resumed session never skips the
		// certificate check (R7-26).
		SessionTicketsDisabled: true,
		GetCertificate:         func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return t.certificate() },
		GetClientCertificate:   func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return t.certificate() },
	}
}

// ---- inbound ----

func (t *TCPTransport) accept() {
	defer t.wg.Done()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			return
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			c.Close()
			return
		}
		ip := ipKey(c.RemoteAddr())
		limit := t.cfg.MaxUnauthenticatedPerIP
		if t.memberIPs[ip] {
			limit = t.cfg.ReservedMemberSlots
		}
		if t.pending[ip] >= limit {
			// Refused before any TLS byte is read (R2-22).
			t.refusedPre++
			t.mu.Unlock()
			c.Close()
			continue
		}
		t.pending[ip]++
		t.raw[c] = struct{}{}
		t.wg.Add(1)
		t.mu.Unlock()
		go t.serve(c)
	}
}

func (t *TCPTransport) serve(raw net.Conn) {
	defer t.wg.Done()
	defer func() {
		raw.Close()
		t.mu.Lock()
		delete(t.raw, raw)
		t.mu.Unlock()
	}()
	var leaf *x509.Certificate
	cfg := t.tlsConfig()
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.ClientCAs = t.cfg.Roots
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		l, err := t.verifyChain(cs, x509.ExtKeyUsageClientAuth)
		leaf = l
		return err
	}
	c := tls.Server(raw, cfg)
	_ = c.SetDeadline(time.Now().Add(t.cfg.HandshakeTimeout))
	ip := ipKey(raw.RemoteAddr())
	authenticated := false
	defer func() {
		if !authenticated {
			t.mu.Lock()
			t.pending[ip]--
			t.mu.Unlock()
		}
	}()
	err := c.Handshake()
	t.mu.Lock()
	t.pending[ip]--
	t.mu.Unlock()
	authenticated = true
	if err != nil {
		return
	}
	var lane [1]byte
	if _, err := io.ReadFull(c, lane[:]); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	spki := spkiOf(leaf)
	_, slot, _ := parseSAN(leaf)
	t.mu.Lock()
	m, member := t.bySPKI[spki]
	if member {
		t.in[c] = spki
	}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.in, c)
		t.mu.Unlock()
	}()
	if lane[0] == laneJoin {
		t.serveJoin(c, coreconsensus.AuthenticatedPeer{Slot: slot, SPKI: spki})
		return
	}
	if !member || m.Slot != slot {
		// Admission is keyed on the member's pinned SPKI and its slot
		// (R8-07): a valid certificate with another key is refused.
		return
	}
	peer := coreconsensus.AuthenticatedPeer{ID: m.ID, Incarnation: m.Incarnation, Slot: slot, SPKI: spki}
	t.mu.Lock()
	t.peerExpiry[m.ID] = leaf.NotAfter
	t.mu.Unlock()
	deadline := time.Now().Add(t.cfg.MaxConnectionAge)
	if leaf.NotAfter.Before(deadline) {
		deadline = leaf.NotAfter
	}
	_ = c.SetReadDeadline(deadline)
	switch lane[0] {
	case laneControl, laneData:
		limit := t.cfg.MaxFrameBytes
		if lane[0] == laneControl {
			limit = t.cfg.MaxControlFrameBytes
		}
		t.readFrames(c, peer, limit)
	case laneSnapshot:
		t.serveSnapshot(c, peer)
	}
}

func (t *TCPTransport) readFrames(c net.Conn, peer coreconsensus.AuthenticatedPeer, limit int) {
	r := bufio.NewReaderSize(c, 64<<10)
	var buf []byte
	for {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return
		}
		n := int(binary.LittleEndian.Uint32(h[:]))
		// The announced length is checked before anything is allocated.
		if n < 10 || n+4 > limit {
			return
		}
		if cap(buf) < n+4 {
			buf = make([]byte, n+4)
		}
		buf = buf[:n+4]
		copy(buf, h[:])
		if _, err := io.ReadFull(r, buf[4:]); err != nil {
			return
		}
		t.mu.Lock()
		d := t.deliver
		_, still := t.bySPKI[peer.SPKI]
		t.mu.Unlock()
		if !still {
			return
		}
		if d != nil {
			d(peer, buf)
		}
	}
}

// ---- outbound ----

func (t *TCPTransport) dial(ctx context.Context, addr string, lane byte, pin *[32]byte) (*tls.Conn, error) {
	d := net.Dialer{Timeout: t.cfg.HandshakeTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cfg := t.tlsConfig()
	cfg.RootCAs = t.cfg.Roots
	// The chain is verified by VerifyConnection itself, roots and extended
	// key usage first, then the SAN and the pin (R2-21): the standard
	// check would look for a DNS name the certificate does not carry.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		leaf, err := t.verifyChain(cs, x509.ExtKeyUsageServerAuth)
		if err != nil {
			return err
		}
		if pin != nil && spkiOf(leaf) != *pin {
			return peerRejected("key does not match the member's pin")
		}
		return nil
	}
	c := tls.Client(raw, cfg)
	_ = c.SetDeadline(time.Now().Add(t.cfg.HandshakeTimeout))
	if err := c.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	if _, err := c.Write([]byte{lane}); err != nil {
		c.Close()
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func (t *TCPTransport) address(to coreconsensus.NodeID) (string, [32]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.byID[to]
	if !ok || m.SPKI == ([32]byte{}) {
		return "", [32]byte{}, false
	}
	addr := m.Address
	if a, ok := t.cfg.Seeds[to]; ok && addr == "" {
		addr = a
	}
	return addr, m.SPKI, addr != ""
}

// Send implements Transport: it never blocks, and drops when the peer's
// queue is full (D4).
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
		pc = &peerConn{queue: make(chan []byte, 4096)}
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
// peer unreachable (D4, R2-06).
func (t *TCPTransport) sender(k outKey, pc *peerConn) {
	defer t.wg.Done()
	var conn *tls.Conn
	var w *bufio.Writer
	var created time.Time
	var backoff time.Time
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for {
		var f []byte
		select {
		case <-t.done:
			return
		case f = <-pc.queue:
		}
		batch := [][]byte{f}
	more:
		for len(batch) < 1024 {
			select {
			case g := <-pc.queue:
				batch = append(batch, g)
			default:
				break more
			}
		}
		size := 0
		for _, b := range batch {
			size += len(b)
		}
		pc.mu.Lock()
		pc.bytes -= int64(size)
		pc.mu.Unlock()
		if conn != nil && time.Since(created) > t.cfg.MaxConnectionAge {
			conn.Close()
			conn = nil
		}
		if conn == nil {
			if time.Now().Before(backoff) {
				continue // dropped: Raft retransmits what matters
			}
			addr, pin, ok := t.address(k.to)
			if !ok {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), t.cfg.HandshakeTimeout)
			c, err := t.dial(ctx, addr, k.lane, &pin)
			cancel()
			if err != nil {
				backoff = time.Now().Add(100 * time.Millisecond)
				t.reportUnreachable(k.to)
				continue
			}
			conn, w, created = c, bufio.NewWriterSize(c, 64<<10), time.Now()
		}
		_ = conn.SetWriteDeadline(time.Now().Add(t.cfg.WriteTimeout))
		var err error
		for _, b := range batch {
			if _, err = w.Write(b); err != nil {
				break
			}
		}
		if err == nil {
			err = w.Flush()
		}
		if err != nil {
			conn.Close()
			conn = nil
			t.reportUnreachable(k.to)
		}
	}
}

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
	deliver func(coreconsensus.AuthenticatedPeer, []byte),
	offer func(coreconsensus.AuthenticatedPeer, coreconsensus.InstallOffer) coreconsensus.InstallAccept,
	install func(coreconsensus.InstallRequest) error,
	join func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deliver, t.offer, t.install, t.join = deliver, offer, install, join
	return nil
}

// SetMembership implements Transport: the admitted set is the union over
// the groups; a member that left has its connections cut and its
// handshakes refused from then on (D4).
func (t *TCPTransport) SetMembership(group coreconsensus.GroupID, admitted []coreconsensus.Member) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.members[group] = append([]coreconsensus.Member(nil), admitted...)
	byID := map[coreconsensus.NodeID]coreconsensus.Member{}
	bySPKI := map[[32]byte]coreconsensus.Member{}
	for _, ms := range t.members {
		for _, m := range ms {
			byID[m.ID] = m
			if m.SPKI != ([32]byte{}) {
				bySPKI[m.SPKI] = m
			}
		}
	}
	t.byID, t.bySPKI = byID, bySPKI
	ips := map[string]bool{}
	for _, m := range byID {
		host, _, err := net.SplitHostPort(m.Address)
		if err != nil {
			continue
		}
		// A member address that does not resolve is refused a reserved
		// slot, never given an unkeyed one.
		addrs, err := net.LookupIP(host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ips[ipKeyOf(a)] = true
		}
	}
	t.memberIPs = ips
	for c, spki := range t.in {
		if _, ok := bySPKI[spki]; !ok {
			c.Close()
		}
	}
}

// Close stops the transport and joins its goroutines.
func (t *TCPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	close(t.done)
	for c := range t.raw {
		c.Close()
	}
	t.mu.Unlock()
	err := t.ln.Close()
	t.wg.Wait()
	return err
}

// ---- snapshots: offer, accept, then the stream (R7-08) ----

func writeChunk(w io.Writer, b []byte) error {
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readChunk(r io.Reader, limit int) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(h[:]))
	if n > limit {
		return nil, origin(coreconsensus.FrameInvalid, errs.Int("length", n))
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

func encodeOffer(o coreconsensus.InstallOffer) []byte {
	b := binary.LittleEndian.AppendUint64(nil, o.Term)
	return wire.AppendSnapshotMeta(b, &o.Meta)
}

func decodeOffer(b []byte) (coreconsensus.InstallOffer, error) {
	if len(b) < 8 {
		return coreconsensus.InstallOffer{}, origin(coreconsensus.FrameInvalid)
	}
	o := coreconsensus.InstallOffer{Term: binary.LittleEndian.Uint64(b)}
	if err := wire.ParseSnapshotMeta(b[8:], &o.Meta); err != nil {
		return o, err
	}
	o.SnapshotID = o.Meta.ID
	return o, nil
}

// Stream implements Transport.
func (t *TCPTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOffer, open func(int64) (io.ReadCloser, error)) error {
	addr, pin, ok := t.address(to)
	if !ok {
		return peerRejected("no address")
	}
	c, err := t.dial(ctx, addr, laneSnapshot, &pin)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
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
	rc, err := open(off)
	if err != nil {
		return err
	}
	_, err = io.Copy(c, rc)
	rc.Close()
	if err != nil {
		return err
	}
	if err := c.CloseWrite(); err != nil {
		return err
	}
	res, err := readChunk(c, 8)
	if err != nil || len(res) != 4 {
		return origin(coreconsensus.FrameInvalid, errs.String("what", "install result"))
	}
	if code := errs.Code(binary.LittleEndian.Uint32(res)); code != 0 {
		return origin(sentinelOf(code))
	}
	return nil
}

func (t *TCPTransport) serveSnapshot(c *tls.Conn, peer coreconsensus.AuthenticatedPeer) {
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
	ierr := in(coreconsensus.InstallRequest{Ctx: ctx, From: peer, Offer: o, Offset: acc.Offset,
		Body: io.LimitReader(c, o.Meta.EncodedSize-acc.Offset)})
	var code errs.Code
	if ierr != nil {
		code, _ = errs.CodeOf(ierr)
		if code == 0 {
			code = coreconsensus.CodeSnapshotCorrupt
		}
	}
	_ = writeChunk(c, binary.LittleEndian.AppendUint32(nil, uint32(code)))
}

// ---- pre-admission JOIN (D4, R7-42, R8-09) ----

func encodeJoin(r coreconsensus.JoinRequest) []byte {
	b := append([]byte(nil), r.Cluster[:]...)
	b = binary.LittleEndian.AppendUint64(b, uint64(r.ID))
	b = append(b, r.Incarnation[:]...)
	b = append(b, byte(len(r.Slot)))
	b = append(b, r.Slot...)
	return append(b, r.Address...)
}

func decodeJoin(b []byte) (coreconsensus.JoinRequest, bool) {
	var r coreconsensus.JoinRequest
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
func (t *TCPTransport) serveJoin(c *tls.Conn, peer coreconsensus.AuthenticatedPeer) {
	_ = c.SetDeadline(time.Now().Add(t.cfg.JoinTimeout))
	b, err := readChunk(c, 512)
	if err != nil {
		return
	}
	req, ok := decodeJoin(b)
	reply := func(code errs.Code, pos coreconsensus.Position) {
		out := binary.LittleEndian.AppendUint32(nil, uint32(code))
		out = binary.LittleEndian.AppendUint64(out, pos.Term)
		out = binary.LittleEndian.AppendUint64(out, pos.Index)
		if code == 0 {
			// The admitted members, so the joiner's transport admits the
			// leader that will feed it before its own log names anyone.
			t.mu.Lock()
			m := coreconsensus.Membership{}
			for _, x := range t.byID {
				m.Members = append(m.Members, x)
			}
			t.mu.Unlock()
			out = wire.AppendMembership(out, &m)
		}
		_ = writeChunk(c, out)
	}
	if !ok || req.Slot != peer.Slot {
		reply(coreconsensus.CodeJoinRefused, coreconsensus.Position{})
		return
	}
	req.SPKI = peer.SPKI
	t.mu.Lock()
	j := t.join
	t.mu.Unlock()
	if j == nil {
		reply(coreconsensus.CodeJoinRefused, coreconsensus.Position{})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.cfg.JoinTimeout)
	defer cancel()
	pos, code := j(ctx, peer, req)
	reply(code, pos)
}

// Join implements Joiner: the JOIN goes to each address in turn until one
// member answers JOIN_ACK with the position its AddLearner was appended at.
func (t *TCPTransport) Join(ctx context.Context, cfg coreconsensus.JoinConfig) (coreconsensus.Position, error) {
	req := coreconsensus.JoinRequest{Cluster: cfg.Stamp.Log.Cluster, ID: cfg.Stamp.ID, Incarnation: cfg.Stamp.Incarnation,
		Slot: t.slot, Address: cfg.Address}
	if req.Cluster != t.cluster {
		return coreconsensus.Position{}, origin(coreconsensus.ClusterMismatch)
	}
	last := error(origin(coreconsensus.JoinRefused, errs.String("why", "no address answered")))
	for _, addr := range cfg.Addresses {
		c, err := t.dial(ctx, addr, laneJoin, nil)
		if err != nil {
			last = err
			continue
		}
		_ = c.SetDeadline(time.Now().Add(t.cfg.JoinTimeout))
		if err := writeChunk(c, encodeJoin(req)); err != nil {
			c.Close()
			last = err
			continue
		}
		b, err := readChunk(c, t.cfg.MaxControlFrameBytes)
		c.Close()
		if err != nil || len(b) < 20 {
			last = origin(coreconsensus.FrameInvalid, errs.String("what", "join answer"))
			continue
		}
		if code := errs.Code(binary.LittleEndian.Uint32(b)); code != 0 {
			last = origin(sentinelOf(code))
			continue
		}
		m, err := wire.ParseMembership(b[20:])
		if err != nil {
			last = err
			continue
		}
		t.SetMembership(1, m.Members)
		return coreconsensus.Position{Term: binary.LittleEndian.Uint64(b[4:]), Index: binary.LittleEndian.Uint64(b[12:])}, nil
	}
	return coreconsensus.Position{}, last
}

// ipKey is the admission key of a remote address: the IPv4 address, or the
// IPv6 /64.
func ipKey(a net.Addr) string {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ipKeyOf(ta.IP)
	}
	return a.String()
}

func ipKeyOf(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// RefusedBeforeHandshake counts the connections refused by the
// pre-handshake admission.
func (t *TCPTransport) RefusedBeforeHandshake() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refusedPre
}

// CertExpiry returns when the transport's own certificate expires.
func (t *TCPTransport) CertExpiry() time.Time {
	c, err := t.certificate()
	if err != nil || len(c.Certificate) == 0 {
		return time.Time{}
	}
	leaf := c.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return time.Time{}
		}
	}
	return leaf.NotAfter
}

// PeerCertExpiry returns when each admitted peer's last-seen certificate
// expires.
func (t *TCPTransport) PeerCertExpiry() map[coreconsensus.NodeID]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[coreconsensus.NodeID]time.Time, len(t.peerExpiry))
	for k, v := range t.peerExpiry {
		out[k] = v
	}
	return out
}

// ReloadFailures counts the refused reloads of a PEM identity, 0 without one.
func (t *TCPTransport) ReloadFailures() int {
	if t.reloads == nil {
		return 0
	}
	return t.reloads()
}
