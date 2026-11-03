package file

import (
	"context"
	"os"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	corefile "github.com/kitsunium/sdk/internal/core/observe/logger/sink/file"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// blockSync makes s's Flush block inside its fsync until release is closed,
// and closes entered once it is there.
func blockSync(s *fileSink) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	s.syncFile = func(f *os.File) error {
		close(entered)
		<-release
		return f.Sync()
	}
	return entered, release
}

// TestAWriteCompletesWhileFlushIsInSync (#815): a Flush blocked in fsync holds
// no lock a Write needs, so the Write returns before the sync does.
func TestAWriteCompletesWhileFlushIsInSync(t *testing.T) {
	t.Parallel()
	s, path := mustOpenSink(t)
	entered, release := blockSync(s)
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush(context.Background()) }()
	<-entered
	wrote := make(chan error, 1)
	go func() {
		_, err := s.Write(context.Background(), corelogger.RecordEvent{Level: level.Info}, []byte("during\n"))
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("Write during Flush: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a Write waited for a Flush blocked in fsync")
	}
	close(release)
	if err := <-flushed; err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "during\n" {
		t.Fatalf("file holds %q", got)
	}
}

// TestCloseDuringFlushNeverHangs (#815): Close while a Flush is in fsync
// returns, and the Flush reports nil or SYNC_FAILED — never hangs.
func TestCloseDuringFlushNeverHangs(t *testing.T) {
	t.Parallel()
	s, _ := mustOpenSink(t)
	entered, release := blockSync(s)
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush(context.Background()) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Close waited for a Flush blocked in fsync")
	}
	close(release)
	select {
	case err := <-flushed:
		if err != nil && !errs.HasCode(err, corefile.CodeSyncFailed) {
			t.Fatalf("Flush racing Close = %v, want nil or SYNC_FAILED", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Flush hung after Close")
	}
}

// TestWriteBatchAppendsEveryRecordInOrder (#807): the batch lands as the
// concatenation of its payloads, in order, and reports every record.
func TestWriteBatchAppendsEveryRecordInOrder(t *testing.T) {
	t.Parallel()
	s, path := mustOpenSink(t)
	recs := make([]corelogger.RecordEvent, 3)
	payloads := [][]byte{[]byte("one\n"), []byte("two\n"), []byte("three\n")}
	written, err := s.WriteBatch(context.Background(), recs, payloads)
	if err != nil || written != 3 {
		t.Fatalf("WriteBatch = %d, %v; want 3, nil", written, err)
	}
	if _, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte("four\n")); err != nil {
		t.Fatalf("Write after batch: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("file holds %q", got)
	}
}

// TestWriteBatchAfterCloseReportsNothingWritten (#807): a refused batch
// reports zero records, so the async drainer reports every one of them.
func TestWriteBatchAfterCloseReportsNothingWritten(t *testing.T) {
	t.Parallel()
	s, _ := mustOpenSink(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	written, err := s.WriteBatch(context.Background(), make([]corelogger.RecordEvent, 2), [][]byte{[]byte("a\n"), []byte("b\n")})
	if written != 0 || !errs.HasCode(err, corefile.CodeWriteFailed) {
		t.Fatalf("WriteBatch after Close = %d, %v; want 0, WRITE_FAILED", written, err)
	}
}

// TestWholeRecordsCountsOnlyCompletePayloads pins the short-write accounting.
func TestWholeRecordsCountsOnlyCompletePayloads(t *testing.T) {
	t.Parallel()
	payloads := [][]byte{[]byte("abc"), []byte("de"), []byte("f")}
	for n, want := range map[int]int{0: 0, 2: 0, 3: 1, 4: 1, 5: 2, 6: 3, 9: 3} {
		if got := wholeRecords(payloads, n); got != want {
			t.Errorf("wholeRecords(%d) = %d, want %d", n, got, want)
		}
	}
}
