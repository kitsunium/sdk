//go:build !windows

package journald

import (
	"context"
	"net"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corejournald "github.com/kitsunium/sdk/internal/core/observe/logger/writer/journald"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestJournaldBatchSendsOneEntryPerRecord (#807): the journald sink is a
// BatchSink; a backlog leaves as one MESSAGE= datagram per record, framed as
// Write frames one, in order — through one sendmmsg(2) on Linux.
func TestJournaldBatchSendsOneEntryPerRecord(t *testing.T) {
	t.Parallel()
	path := shortSocketPath(t)
	journal, err := net.ListenPacket(socketNetwork, path)
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { ignoreClose(journal.Close()) })
	conn, err := net.Dial(socketNetwork, path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	var sink corelogger.Sink = newJournaldSink(conn)
	t.Cleanup(func() { ignoreClose(sink.Close()) })
	batch, ok := sink.(corelogger.BatchSink)
	if !ok {
		t.Fatal("the journald sink is not a BatchSink")
	}
	payloads := [][]byte{[]byte("one\n"), []byte("two"), []byte("three\n")}
	written, werr := batch.WriteBatch(context.Background(), make([]corelogger.RecordEvent, 3), payloads)
	if werr != nil || written != 3 {
		t.Fatalf("WriteBatch = %d, %v; want 3, nil", written, werr)
	}
	if err := journal.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	for _, want := range []string{"MESSAGE=one\n", "MESSAGE=two\n", "MESSAGE=three\n"} {
		n, _, rerr := journal.ReadFrom(buf)
		if rerr != nil || string(buf[:n]) != want {
			t.Fatalf("datagram = %q, %v; want %q", buf[:n], rerr, want)
		}
	}
}

// TestJournaldBatchOnAClosedSocketFailsUnderItsCode: a refused batch counts
// nothing delivered and surfaces under the journald write code.
func TestJournaldBatchOnAClosedSocketFailsUnderItsCode(t *testing.T) {
	t.Parallel()
	path := shortSocketPath(t)
	journal, err := net.ListenPacket(socketNetwork, path)
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { ignoreClose(journal.Close()) })
	conn, err := net.Dial(socketNetwork, path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	s := newJournaldSink(conn)
	ignoreClose(s.Close())
	written, werr := s.WriteBatch(context.Background(), make([]corelogger.RecordEvent, 2), [][]byte{[]byte("a"), []byte("b")})
	if written != 0 || !errs.HasCode(werr, corejournald.CodeJournaldWriteFailed) {
		t.Fatalf("WriteBatch on a closed socket = %d, %v; want 0 under the write code", written, werr)
	}
}
