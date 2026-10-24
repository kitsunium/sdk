package consensus

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// offerPair is a sender L and a receiver F of snapshot streams over TCP,
// whose receiver counts the offers it judged and the installs it ran, and
// logs what its transport refused.
type offerPair struct {
	trL, trF           *TCPTransport
	members            []coreconsensus.MemberValue
	offered, installed atomic.Int64
	log                warnLines
}

// warnLines records the messages a transport logs through bindWarn.
type warnLines struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnLines) warn(msg string, _ ...corelogger.AttrValue) {
	w.mu.Lock()
	w.lines = append(w.lines, msg)
	w.mu.Unlock()
}

// logged reports whether a line containing s was logged within a second:
// the receiver logs on its own connection's goroutine.
func (w *warnLines) logged(s string) bool {
	for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
		w.mu.Lock()
		ok := slices.ContainsFunc(w.lines, func(l string) bool { return strings.Contains(l, s) })
		w.mu.Unlock()
		if ok || time.Now().After(deadline) {
			return ok
		}
	}
}

// newOfferPair starts the pair; a zero frame bound is the default.
func newOfferPair(t *testing.T, sendFrame, recvFrame int) *offerPair {
	t.Helper()
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xa9}
	certL, spkiL := ca.issue(t, SAN(cid, "site-l"), nil)
	certF, spkiF := ca.issue(t, SAN(cid, "site-f"), nil)
	open := func(cert *tls.Certificate, frame int) *TCPTransport {
		tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0", MaxFrameBytes: frame})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tr.Close() })
		return tr
	}
	p := &offerPair{trL: open(certL, sendFrame), trF: open(certF, recvFrame)}
	p.members = []coreconsensus.MemberValue{
		{ID: 1, Slot: "site-l", SPKI: spkiL, Address: p.trL.Addr(), Voter: true},
		{ID: 2, Slot: "site-f", SPKI: spkiF, Address: p.trF.Addr(), Voter: true},
	}
	p.trL.SetMembership(1, p.members)
	p.trF.SetMembership(1, p.members)
	p.trF.bindWarn(p.log.warn)
	if err := p.trF.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {}, p.offer, p.install, nil); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *offerPair) offer(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
	p.offered.Add(1)
	return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID}
}

func (p *offerPair) install(ip coreconsensus.InstallParams) error {
	_, err := io.Copy(io.Discard, ip.Body)
	p.installed.Add(1)
	return err
}

// stream sends o from L to F and returns the stream's verdict, the
// counters reset first.
func (p *offerPair) stream(o coreconsensus.InstallOfferValue) error {
	p.offered.Store(0)
	p.installed.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.trL.Stream(ctx, 2, o, retiredBody)
}

// retiredSnapshot is the body every offer of these tests streams.
var retiredSnapshot = []byte("snapshot")

func retiredBody(off int64) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(retiredSnapshot[off:])), nil
}

// retiredOffer is an offer of a snapshot at 10 whose configuration lists
// members and retired retired IDs.
func retiredOffer(members []coreconsensus.MemberValue, retired int) coreconsensus.InstallOfferValue {
	m := coreconsensus.MembershipValue{Index: 10, Members: members}
	for i := range retired {
		m.Retired = append(m.Retired, coreconsensus.NodeID(1000+i))
	}
	size := int64(len(retiredSnapshot))
	meta := coreconsensus.SnapshotMetaValue{
		ID: "snap-10-1", Version: 1, Position: coreconsensus.PositionValue{Term: 1, Index: 10},
		Membership: m, Codec: "none", EncodedSize: size, DecodedSize: size,
	}
	return coreconsensus.InstallOfferValue{Term: 1, SnapshotID: meta.ID, Meta: meta}
}

// whatOf returns the "what" field of err, "" when it has none.
func whatOf(err error) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "what" {
			return f.StringValue()
		}
	}
	return ""
}

// #634: every Remove appends the removed ID to Membership.Retired for good,
// and a snapshot's metadata carries the configuration at its index, Retired
// included, 8 bytes per ID. The TCP receiver read the offer under
// MaxControlFrameBytes (64 KiB by default) and ended the connection
// unanswered past it: past about 8 100 removals over a cluster's life, no
// snapshot was ever installed over TCP again, FRAME_INVALID "install accept"
// at every probe. The snapshot lane now reads the offer under MaxFrameBytes,
// the bound a membership entry already meets.
func TestASnapshotOfferCarryingManyRetiredIDsIsInstalledOverTCP(t *testing.T) {
	p := newOfferPair(t, 0, 0)
	runCase := func(t *testing.T, n int) {
		o := retiredOffer(p.members, n)
		err := p.stream(o)
		if err != nil || p.offered.Load() != 1 || p.installed.Load() != 1 {
			t.Errorf("TCP Stream, %d retired IDs (offer %d bytes, control bound %d): %v %q, offered %d, installed %d",
				n, len(encodeOffer(o)), 64<<10, err, whatOf(err), p.offered.Load(), p.installed.Load())
		}
	}
	threshold := (64<<10 - len(encodeOffer(retiredOffer(p.members, 0)))) / 8
	for _, n := range []int{10, threshold, threshold + 1, 9000} {
		t.Run(fmt.Sprintf("%d retired IDs", n), func(t *testing.T) { runCase(t, n) })
	}
	if p.log.logged("snapshot offer refused") {
		t.Errorf("an offer under MaxFrameBytes was logged as refused: %v", p.log.lines)
	}
}

// #634: an offer above the snapshot lane's bound is still refused, now
// with a line in the receiver's log, which said nothing; a sender refuses
// one above its own bound before it dials; and the memory double applies
// the TCP transport's bound, where it handed any offer over as a value and
// the tests never saw the limit.
func TestASnapshotOfferAboveTheFrameBoundIsRefusedAndLogged(t *testing.T) {
	const frame = 256 << 10
	big := 40000 // 320 000 bytes of retired IDs
	t.Run("receiver", func(t *testing.T) {
		p := newOfferPair(t, 0, frame)
		if err := p.stream(retiredOffer(p.members, big)); err == nil || p.offered.Load() != 0 {
			t.Fatalf("an offer above the receiver's MaxFrameBytes: %v, offered %d", err, p.offered.Load())
		}
		if !p.log.logged("snapshot offer refused") {
			t.Errorf("the receiver refused an offer above its bound without a line: %v", p.log.lines)
		}
		if err := p.stream(retiredOffer(p.members, 20000)); err != nil || p.installed.Load() != 1 {
			t.Errorf("an offer under the receiver's MaxFrameBytes: %v, installed %d", err, p.installed.Load())
		}
	})
	t.Run("sender", func(t *testing.T) {
		p := newOfferPair(t, frame, 0)
		err := p.stream(retiredOffer(p.members, big))
		if c, _ := errs.CodeOf(err); c != coreconsensus.CodeFrameInvalid || whatOf(err) != "snapshot offer" || p.offered.Load() != 0 {
			t.Fatalf("an offer above the sender's own MaxFrameBytes: %v %q, offered %d", err, whatOf(err), p.offered.Load())
		}
		if p.log.logged("snapshot offer refused") {
			t.Errorf("an offer the sender kept reached the receiver: %v", p.log.lines)
		}
	})
	t.Run("memory double", func(t *testing.T) {
		net := NewMemoryNetwork()
		ml, mf := NewMemoryTransport(net, 1), NewMemoryTransport(net, 2)
		var p offerPair
		mf.bindWarn(p.log.warn)
		if err := mf.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {}, p.offer, p.install, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = mf.Close() })
		o := retiredOffer(nil, 9000)
		if err := ml.Stream(context.Background(), 2, o, retiredBody); err != nil || p.installed.Load() != 1 {
			t.Fatalf("the double under its default bound: %v, installed %d", err, p.installed.Load())
		}
		mf.mu.Lock()
		mf.maxOffer = len(encodeOffer(o)) - 1
		mf.mu.Unlock()
		err := ml.Stream(context.Background(), 2, o, retiredBody)
		if c, _ := errs.CodeOf(err); c != coreconsensus.CodeFrameInvalid || p.offered.Load() != 1 || !p.log.logged("snapshot offer refused") {
			t.Fatalf("the double above its bound: %v, offered %d, logged %v", err, p.offered.Load(), p.log.lines)
		}
	})
}
