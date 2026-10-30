package consensus

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exclusiveMeta is the metadata of a one-byte snapshot at idx, in term 2,
// as publishMeta publishes it.
func exclusiveMeta(idx uint64) coreconsensus.SnapshotMetaValue {
	return coreconsensus.SnapshotMetaValue{Version: 1, Position: coreconsensus.PositionValue{Index: idx, Term: 2}, ChunkSize: defaultChunkSize}
}

// publishOnce publishes in s a one-byte snapshot under meta, as
// publishMeta does, and returns what failed instead of stopping the test:
// it runs on a goroutine of its own.
func publishOnce(s *fileSnapshots, meta coreconsensus.SnapshotMetaValue) error {
	meta.ID, meta.EncodedSize = snapshotID(meta.Position.Index, meta.Position.Term), 1
	sink, _, err := s.Create(meta.ID, meta)
	//: no spool, nothing to publish.
	if err != nil {
		return err
	}
	//: a spool that could not be written is aborted, never committed.
	if _, err := sink.Write([]byte{0}); err != nil {
		return errors.Join(err, sink.Abort())
	}
	return sink.Commit(meta)
}

// TestAListingNeverMeetsAPublicationOrARemovalOfTheSameStore is #768: the
// node lists its snapshot store and opens a snapshot on its loop while a
// cut publishes and retention removes on another goroutine. Windows
// refuses a rename over, or a removal of, a file another handle holds
// open, and fails the open of a file being renamed over or removed: a
// listing that read a .meta while a cut published over it, or while
// retention removed it, failed one side STORAGE_FAILED, and when that side
// was the cut or the retention, the node stopped. On the Windows runner a
// thousand publications under a listing had 154 refused and 355 listings
// fail; a snapshot handle unpinned before its file was closed let a
// removal meet it too. The store now holds its lock over every file a
// listing, an Open, a publication's rename and a removal touch, and a
// handle closes its file before its pin drops.
//
// The first half holds the store's lock and checks that each of them
// waits for it, on every kernel. The second runs them side by side, as
// the node does, and requires that no side fail STORAGE_FAILED: POSIX
// lets every interleaving through, Windows only once they are exclusive.
// Everything is in t.TempDir().
func TestAListingNeverMeetsAPublicationOrARemovalOfTheSameStore(t *testing.T) {
	s, err := newFileSnapshots(t.TempDir(), storageFailed)
	//: a store that cannot open ends the test.
	if err != nil {
		t.Fatal(err)
	}
	//: two published snapshots, 20 to open and 40 to republish and remove.
	for _, idx := range []uint64{20, 40} {
		//: a failed publication ends the test before any step runs.
		if err := publishOnce(s, exclusiveMeta(idx)); err != nil {
			t.Fatal(err)
		}
	}
	steps := map[string]func() error{
		"a listing": func() error { _, err := s.List(); return err },
		"an Open": func() error {
			h, err := s.Open(snapshotID(20, 2))
			//: no handle, nothing to close.
			if err != nil {
				return err
			}
			return h.Close()
		},
		"a publication": func() error { return publishOnce(s, exclusiveMeta(40)) },
		"a removal":     func() error { return s.Remove(snapshotID(40, 2)) },
	}
	//: each step must wait while the store's lock is held.
	for _, name := range []string{"a listing", "an Open", "a publication", "a removal"} {
		s.mu.Lock()
		done := make(chan error, 1)
		go func() { done <- steps[name]() }()
		//: the step ends under the lock, or it waits for it.
		select {
		//: it ended under the lock: it touched the files without it.
		case err := <-done:
			s.mu.Unlock()
			t.Fatalf("%s went on while the store's lock was held (%v)", name, err)
		//: no end within 50 ms: it waits for the lock.
		case <-time.After(50 * time.Millisecond):
		}
		s.mu.Unlock()
		//: once the lock is released, the step ends without error.
		select {
		//: it ended: it must have succeeded.
		case err := <-done:
			//: a step that failed once let through ends the test.
			if err != nil {
				t.Fatalf("%s: %v %v", name, err, errs.FieldsOf(err))
			}
		//: it never ended: the lock was not released to it.
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never ended once the store's lock was released", name)
		}
	}

	// Side by side: one goroutine republishes 40, publishes a fresh id at
	// each round and removes the ones before it; the other lists, and
	// opens and reads the newest fresh one it listed.
	//: 40 is published again, as the removal step took it.
	if err := publishOnce(s, exclusiveMeta(40)); err != nil {
		t.Fatal(err)
	}
	failed := func(side string, err error) {
		//: only a disk that failed is a failure; a refused pin or a
		//: missing meta is the store's normal answer.
		if errs.HasCode(err, coreconsensus.CodeStorageFailed) {
			t.Errorf("%s failed STORAGE_FAILED beside the other side: %v %v", side, err, errs.FieldsOf(err))
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		//: the reader lists and reads until the writer is done.
		for {
			//: stop once the writer closed stop, without blocking.
			select {
			//: the writer is done.
			case <-stop:
				return
			//: the writer still runs: list again.
			default:
			}
			metas, err := s.List()
			//: a listing that failed ends the reader.
			if err != nil {
				failed("a listing", err)
				return
			}
			//: no fresh snapshot listed yet: list again.
			if len(metas) == 0 || metas[0].Position.Index < 100 {
				continue
			}
			// Removed since it was listed, it is no meta: never a disk
			// that failed.
			h, err := s.Open(metas[0].ID)
			//: no handle, nothing to read or close.
			if err != nil {
				failed("an Open", err)
				continue
			}
			//: a read reaching the end is no failure.
			if _, err := h.ReadAt(make([]byte, 1), 0); err != nil && !errors.Is(err, io.EOF) {
				failed("a read", err)
			}
			//: a close that failed is reported.
			if err := h.Close(); err != nil {
				failed("a Close", err)
			}
		}
	})
	var held []uint64
	//: a hundred rounds of the writer beside the reader.
	for i := range uint64(100) {
		//: a republication that failed ends the writer.
		if err := publishOnce(s, exclusiveMeta(40)); err != nil {
			failed("a publication over the same id", err)
			break
		}
		//: a fresh publication that failed ends the writer.
		if err := publishOnce(s, exclusiveMeta(100+i)); err != nil {
			failed("a publication", err)
			break
		}
		kept := held[:0]
		//: remove every fresh id published before this round.
		for _, idx := range held {
			// A removal the reader's pin refuses is tried again at the
			// next round, as retention does at the next cut.
			//: a refused removal is kept for the next round.
			if err := s.Remove(snapshotID(idx, 2)); err != nil {
				failed("a removal", err)
				kept = append(kept, idx)
			}
		}
		held = append(kept, 100+i)
	}
	close(stop)
	wg.Wait()
}
