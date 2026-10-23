package consensus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// halfCloser is a net.Conn whose half-close does nothing: net.Pipe has
// none.
type halfCloser struct{ net.Conn }

func (halfCloser) CloseWrite() error { return nil }

// A snapshot stream to a receiver that stops reading after the offer had no
// deadline: the write blocked on the full window until the kernel gave up,
// about 15 min on Linux, the peer got no other snapshot and the transfer
// pinned compaction meanwhile (#594). Every write of the stream now fails
// within its own deadline. The receiver here is a conn that never reads.
func TestASnapshotStreamToAReceiverThatStopsReadingFails(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close(); _ = remote.Close() }()
	w := boundedWriter{conn: local, half: halfCloser{local}, d: 100 * time.Millisecond}
	open := func(int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, 1<<20))), nil
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- streamFrom(w, open, 0) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("a stream to a receiver that stopped reading ended with %v, want its write deadline", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("the stream took %v to fail, want about its 100 ms deadline", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stream to a receiver that stopped reading still blocked 5 s later")
	}
}

// A receiver that keeps reading, however slowly, is not cut: the deadline
// runs per write, not over the whole stream.
func TestASlowSnapshotStreamThatKeepsMovingSucceeds(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close(); _ = remote.Close() }()
	w := boundedWriter{conn: local, half: halfCloser{local}, d: 200 * time.Millisecond}
	const size = 8 << 15
	open := func(int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, size))), nil
	}
	go func() {
		buf := make([]byte, 1<<15)
		for {
			time.Sleep(50 * time.Millisecond)
			if _, err := remote.Read(buf); err != nil {
				return
			}
		}
	}()
	if err := streamFrom(w, open, 0); err != nil {
		t.Fatalf("a stream whose receiver kept reading failed: %v", err)
	}
}

// A receiver that reads every byte of a stream and never sends its verdict
// held the sender in installResult, which reads with no deadline, and the
// stream's pin held the sender's compaction meanwhile (#602). The node
// cancels a stream whose verdict is overdue (pinAt), and the cancellation
// ends the read of the verdict over TCP. The receiver's install here never
// returns.
func TestTCPStreamWhoseReceiverNeverAnswersEndsWithItsContext(t *testing.T) {
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
	const size = 64 << 10
	read := make(chan struct{})
	never := make(chan struct{})
	defer close(never)
	accept := func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
		return coreconsensus.InstallAcceptValue{}
	}
	install := func(p coreconsensus.InstallParams) error {
		if _, err := io.Copy(io.Discard, p.Body); err != nil {
			return err
		}
		close(read)
		<-never
		return nil
	}
	if err := b.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {}, accept, install, nil); err != nil {
		t.Fatal(err)
	}
	a.tr.SetMembership(1, members)
	b.tr.SetMembership(1, members)
	offer := coreconsensus.InstallOfferValue{Term: 1, SnapshotID: "s", Meta: coreconsensus.SnapshotMetaValue{ID: "s", Version: 1, EncodedSize: size}}
	open := func(off int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, size-off))), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.tr.Stream(ctx, b.id, offer, open) }()
	select {
	case <-read:
	case err := <-done:
		t.Fatalf("the stream ended before the receiver read it: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver never read the stream")
	}
	select {
	case err := <-done:
		t.Fatalf("the stream ended with %v before any verdict", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a stream cancelled before its verdict reported an install")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled stream still waited for the verdict 2 s later")
	}
}
