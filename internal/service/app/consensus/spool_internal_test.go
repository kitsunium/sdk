package consensus

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// spools is the ids of every receive spool in dir, sorted.
func spools(t *testing.T, dir string) []string {
	t.Helper()
	names := must(filepath.Glob(filepath.Join(dir, "*.spool")))(t)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Base(n))
	}
	slices.Sort(out)
	return out
}

// chunk is one valid chunk of the snapshot stream holding payload.
func chunk(payload []byte) []byte {
	var h [chunkHeaderLen]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(h[8:], crc32.Checksum(payload, castagnoli))
	return append(h[:], payload...)
}

// brokenTransfer leaves a spool for id as a transfer cut mid-stream does:
// one whole chunk, then the sink released for a resume.
func brokenTransfer(t *testing.T, s *fileSnapshots, id string) {
	t.Helper()
	sink, _ := must2(s.Create(id, coreconsensus.SnapshotMetaValue{ID: id}))(t)
	if _, err := sink.Write(chunk([]byte("part of " + id))); err != nil {
		t.Fatal(err)
	}
	sink.(*fileSink).release()
}

func must2[A, B any](a A, b B, err error) func(testing.TB) (A, B) {
	return func(t testing.TB) (A, B) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
}

// A transfer cut mid-stream keeps its spool for a resume of the same id.
// Once an offer of another id is accepted, or the directory reopened past
// a newer one, nothing resumes it any more: it was a full snapshot left on
// the data disk for good, one more at every such cut (#527).
func TestAnOrphanSpoolIsRemoved(t *testing.T) {
	dir := t.TempDir()
	s := must(newFileSnapshots(dir))(t)
	x := "00000000000000000333-00000000000000000002"
	y := "00000000000000000383-00000000000000000002"
	brokenTransfer(t, s, x)
	if got := spools(t, dir); !slices.Equal(got, []string{x + ".spool"}) {
		t.Fatalf("spools after a broken transfer %v, want X kept for a resume", got)
	}
	// The same id resumes from its whole chunk.
	sink, off := must2(s.Create(x, coreconsensus.SnapshotMetaValue{ID: x}))(t)
	if want := int64(chunkHeaderLen + len("part of "+x)); off != want {
		t.Fatalf("resume offset %d, want %d", off, want)
	}
	sink.(*fileSink).release()
	// Another id accepted: X is an orphan.
	brokenTransfer(t, s, y)
	if got := spools(t, dir); !slices.Equal(got, []string{y + ".spool"}) {
		t.Fatalf("spools once another id is accepted %v, want only Y", got)
	}
	// A spool a sink still writes to is never removed under it.
	z := "00000000000000000400-00000000000000000003"
	open, _ := must2(s.Create(z, coreconsensus.SnapshotMetaValue{ID: z}))(t)
	w := "00000000000000000401-00000000000000000003"
	brokenTransfer(t, s, w)
	if got := spools(t, dir); !slices.Equal(got, []string{z + ".spool", w + ".spool"}) {
		t.Fatalf("spools with a sink open on Z %v, want Z and W", got)
	}
	abandon(open.Abort())
	// A reopen keeps the newest spool only.
	if err := os.WriteFile(filepath.Join(dir, x+".spool"), chunk([]byte("old")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newFileSnapshots(dir); err != nil {
		t.Fatal(err)
	}
	if got := spools(t, dir); !slices.Equal(got, []string{w + ".spool"}) {
		t.Fatalf("spools after a reopen %v, want only the newest", got)
	}
}

// The chunk size a resume reads by comes from the peer's offer: one near
// the int64 limit overflowed the encoded bound into a negative buffer size,
// and the resume panicked on the event loop.
func TestAResumeBoundsTheChunkSizeAnOfferNames(t *testing.T) {
	dir := t.TempDir()
	s := must(newFileSnapshots(dir))(t)
	id := "00000000000000000333-00000000000000000002"
	brokenTransfer(t, s, id)
	for _, size := range []int64{math.MaxInt64, math.MaxInt64 / 8 * 7, -1} {
		sink, off := must2(s.Create(id, coreconsensus.SnapshotMetaValue{ID: id, ChunkSize: size}))(t)
		sink.(*fileSink).release()
		if want := int64(chunkHeaderLen + len("part of "+id)); off != want {
			t.Fatalf("chunk size %d: resume offset %d, want %d", size, off, want)
		}
	}
}

// A resume finds its offset reading the spool in bounded pieces: it read
// the whole spool into memory, on the event loop, and allocated more than
// the spool's size (#527).
func TestAResumeReadsItsSpoolInBoundedMemory(t *testing.T) {
	dir := t.TempDir()
	s := must(newFileSnapshots(dir))(t)
	id := "00000000000000000333-00000000000000000002"
	payload := make([]byte, defaultChunkSize)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	c := chunk(payload)
	f := must(os.Create(filepath.Join(dir, id+".spool")))(t)
	const chunks = 16
	for range chunks {
		if _, err := f.Write(c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Write(c[:len(c)/2]); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	size := int64(chunks*len(c) + len(c)/2)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	sink, off := must2(s.Create(id, coreconsensus.SnapshotMetaValue{ID: id, ChunkSize: defaultChunkSize}))(t)
	runtime.ReadMemStats(&after)
	sink.(*fileSink).release()
	if want := int64(chunks * len(c)); off != want {
		t.Fatalf("resume offset %d, want %d", off, want)
	}
	if got := int64(after.TotalAlloc - before.TotalAlloc); got > size/4 {
		t.Fatalf("a resume over a %d-byte spool allocated %d bytes, want it bounded", size, got)
	}
}
