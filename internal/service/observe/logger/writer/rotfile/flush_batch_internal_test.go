package rotfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// blockSync makes s's Flush block inside its fsync until release is closed,
// and closes entered once it is there.
func blockSync(s *rotatingSink) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	s.syncFile = func(f *os.File) error {
		close(entered)
		<-release
		return f.Sync()
	}
	return entered, release
}

// TestAWriteCompletesWhileFlushIsInSync (#815): a Flush blocked in fsync holds
// no lock a Write needs — a Write that does not rotate returns before it.
func TestAWriteCompletesWhileFlushIsInSync(t *testing.T) {
	t.Parallel()
	s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "a.log")})
	entered, release := blockSync(s)
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush(context.Background()) }()
	<-entered
	wrote := make(chan error, 1)
	go func() {
		_, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte("during\n"))
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
}

// TestARotationWaitsForTheFsyncOfTheFileItCloses (#815): the descriptor a
// Flush is syncing is pinned — a rotation that would close it waits for the
// sync, then rotates; nothing written is lost and the Flush reports nil.
func TestARotationWaitsForTheFsyncOfTheFileItCloses(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "r.log")
	s := newSink(t, Config{Path: path, MaxBytes: 8, MaxBackups: 2})
	if _, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entered, release := blockSync(s)
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush(context.Background()) }()
	<-entered
	rotated := make(chan error, 1)
	go func() {
		_, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte("second\n"))
		rotated <- err
	}()
	select {
	case <-rotated:
		t.Fatal("the rotation closed the descriptor a Flush was syncing")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-flushed; err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := <-rotated; err != nil {
		t.Fatalf("rotating Write: %v", err)
	}
	backup, _ := os.ReadFile(path + ".1")
	active, _ := os.ReadFile(path)
	if string(backup) != "first\n" || string(active) != "second\n" {
		t.Fatalf("backup %q, active %q", backup, active)
	}
}

// TestCloseDuringFlushNeverHangs (#815): Close while a Flush is in fsync
// returns, and the Flush reports nil or ROT_FILE_WRITE_FAILED — never hangs.
func TestCloseDuringFlushNeverHangs(t *testing.T) {
	t.Parallel()
	s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "c.log")})
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
		if err != nil && !errs.HasCode(err, corerotfile.CodeRotFileWriteFailed) {
			t.Fatalf("Flush racing Close = %v, want nil or ROT_FILE_WRITE_FAILED", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Flush hung after Close")
	}
}

// TestWriteBatchRotatesOnceAndWritesInOrder (#807): a batch is checked
// against MaxBytes once, lands in one write in order, and reports every record.
func TestWriteBatchRotatesOnceAndWritesInOrder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.log")
	s := newSink(t, Config{Path: path, MaxBytes: 16, MaxBackups: 3})
	if _, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte("old record\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	payloads := [][]byte{[]byte("one\n"), []byte("two\n"), []byte("three\n")}
	written, err := s.WriteBatch(context.Background(), make([]corelogger.RecordEvent, 3), payloads)
	if err != nil || written != 3 {
		t.Fatalf("WriteBatch = %d, %v; want 3, nil", written, err)
	}
	backup, _ := os.ReadFile(path + ".1")
	active, _ := os.ReadFile(path)
	if string(backup) != "old record\n" || string(active) != "one\ntwo\nthree\n" {
		t.Fatalf("backup %q, active %q", backup, active)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("a batch rotated more than once: %v", err)
	}
	if strings.Count(string(active), "\n") != 3 {
		t.Fatalf("active %q", active)
	}
}
