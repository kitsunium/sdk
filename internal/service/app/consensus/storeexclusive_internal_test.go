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
	if err != nil {
		return err
	}
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
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []uint64{20, 40} {
		if err := publishOnce(s, exclusiveMeta(idx)); err != nil {
			t.Fatal(err)
		}
	}
	steps := map[string]func() error{
		"a listing": func() error { _, err := s.List(); return err },
		"an Open": func() error {
			h, err := s.Open(snapshotID(20, 2))
			if err != nil {
				return err
			}
			return h.Close()
		},
		"a publication": func() error { return publishOnce(s, exclusiveMeta(40)) },
		"a removal":     func() error { return s.Remove(snapshotID(40, 2)) },
	}
	for _, name := range []string{"a listing", "an Open", "a publication", "a removal"} {
		s.mu.Lock()
		done := make(chan error, 1)
		go func() { done <- steps[name]() }()
		select {
		case err := <-done:
			s.mu.Unlock()
			t.Fatalf("%s went on while the store's lock was held (%v)", name, err)
		case <-time.After(50 * time.Millisecond):
		}
		s.mu.Unlock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v %v", name, err, errs.FieldsOf(err))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never ended once the store's lock was released", name)
		}
	}

	// Side by side: one goroutine republishes 40, publishes a fresh id at
	// each round and removes the ones before it; the other lists, and
	// opens and reads the newest fresh one it listed.
	if err := publishOnce(s, exclusiveMeta(40)); err != nil {
		t.Fatal(err)
	}
	failed := func(side string, err error) {
		if errs.HasCode(err, coreconsensus.CodeStorageFailed) {
			t.Errorf("%s failed STORAGE_FAILED beside the other side: %v %v", side, err, errs.FieldsOf(err))
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			metas, err := s.List()
			if err != nil {
				failed("a listing", err)
				return
			}
			if len(metas) == 0 || metas[0].Position.Index < 100 {
				continue
			}
			// Removed since it was listed, it is no meta: never a disk
			// that failed.
			h, err := s.Open(metas[0].ID)
			if err != nil {
				failed("an Open", err)
				continue
			}
			if _, err := h.ReadAt(make([]byte, 1), 0); err != nil && !errors.Is(err, io.EOF) {
				failed("a read", err)
			}
			if err := h.Close(); err != nil {
				failed("a Close", err)
			}
		}
	})
	var held []uint64
	for i := range uint64(100) {
		if err := publishOnce(s, exclusiveMeta(40)); err != nil {
			failed("a publication over the same id", err)
			break
		}
		if err := publishOnce(s, exclusiveMeta(100+i)); err != nil {
			failed("a publication", err)
			break
		}
		kept := held[:0]
		for _, idx := range held {
			// A removal the reader's pin refuses is tried again at the
			// next round, as retention does at the next cut.
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
