package nettransport

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corenettransport "github.com/kitsunium/sdk/internal/core/observe/logger/writer/nettransport"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTCPBatchShipsTheBacklogInOrder (#807): the tcp batch seam writes every
// payload, in order, as the stream bytes the record-at-a-time seam writes.
func TestTCPBatchShipsTheBacklogInOrder(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	read := make(chan string, 1)
	go func() {
		got, _ := io.ReadAll(server)
		read <- string(got)
	}()
	send, sendBatch, closer, err := newConnSeam("tcp", "pipe", func(string, string) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatalf("newConnSeams: %v", err)
	}
	s := newNetSink("tcp", send, closer)
	s.sendBatch = sendBatch
	payloads := [][]byte{[]byte("one\n"), []byte("two\n"), []byte("three\n")}
	written, werr := s.WriteBatch(t.Context(), make([]corelogger.RecordEvent, 3), payloads)
	if werr != nil || written != 3 {
		t.Fatalf("WriteBatch = %d, %v; want 3, nil", written, werr)
	}
	if string(payloads[0]) != "one\n" {
		t.Fatalf("the caller's payloads were consumed: %q", payloads[0])
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if got := <-read; got != "one\ntwo\nthree\n" {
		t.Fatalf("the peer read %q", got)
	}
}

// TestBatchWithoutABatchSeamSendsRecordByRecord: a seam with no batch call
// (udp, http) receives one send per record, and the first refusal ends the
// batch with the records before it counted.
func TestBatchWithoutABatchSeamSendsRecordByRecord(t *testing.T) {
	t.Parallel()
	refuse := errors.New("refused")
	var sent []string
	s := newNetSink("udp", func(_ context.Context, p []byte) error {
		if string(p) == "bad" {
			return refuse
		}
		sent = append(sent, string(p))
		return nil
	}, nil)
	payloads := [][]byte{[]byte("a"), []byte("b"), []byte("bad"), []byte("c")}
	written, err := s.WriteBatch(context.Background(), make([]corelogger.RecordEvent, 4), payloads)
	if written != 2 || !errors.Is(err, refuse) || !errs.HasCode(err, corenettransport.CodeNetTransportWriteFailed) {
		t.Fatalf("WriteBatch = %d, %v; want 2 and the refusal under the write code", written, err)
	}
	if len(sent) != 2 || sent[0] != "a" || sent[1] != "b" {
		t.Fatalf("sent %v", sent)
	}
}

// TestWholeRecordsCountsOnlyCompletePayloads pins the short-write accounting.
func TestWholeRecordsCountsOnlyCompletePayloads(t *testing.T) {
	t.Parallel()
	payloads := [][]byte{[]byte("abc"), []byte("de"), []byte("f")}
	for n, want := range map[int64]int{0: 0, 2: 0, 3: 1, 4: 1, 5: 2, 6: 3, 9: 3} {
		if got := wholeRecords(payloads, n); got != want {
			t.Errorf("wholeRecords(%d) = %d, want %d", n, got, want)
		}
	}
}
