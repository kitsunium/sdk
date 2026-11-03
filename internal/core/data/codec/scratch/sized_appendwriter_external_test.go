package scratch_test

import (
	"bytes"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// TestASizedBufferLendsItsRoomToAnAppendWriter is where the size-classed tier
// (#906) and the append writer (#1124) meet: a codec encoding into a buffer of
// a size class can hand that buffer's room to an AppendWriter, as an Append
// hands it a caller's dst. A write within the class's capacity lands in the
// buffer's own array, Truncate gives the prefix back in that same array, and
// once the writer and then the buffer are released, the class hands the next
// hint of its size a clean buffer with room for it.
func TestASizedBufferLendsItsRoomToAnAppendWriter(t *testing.T) {
	t.Parallel()
	for _, hint := range []int{
		scratch.MaxRetainedBufBytes + 1, 1 << 20,
		1<<20 + 1, scratch.MaxRetainedSizedBytes,
	} {
		buf := scratch.AcquireBufferSized(hint)
		buf.WriteString("head")
		room := buf.Bytes()
		w := scratch.AcquireAppendWriter(room)
		if _, err := w.Write(bytes.Repeat([]byte{'r'}, hint-len("head"))); err != nil {
			t.Fatalf("hint %d: Write: %v", hint, err)
		}
		got := w.Bytes()
		if len(got) != hint || string(got[:len("head")]) != "head" {
			t.Fatalf("hint %d: %d bytes, prefix %q; want %d after the prefix", hint, len(got), got[:min(len(got), 4)], hint)
		}
		if &got[0] != &room[0] {
			t.Fatalf("hint %d: a write within the class's capacity left the buffer's array", hint)
		}
		back := w.Truncate()
		if string(back) != "head" || &back[0] != &room[0] {
			t.Fatalf("hint %d: Truncate = %q, want the prefix in the buffer's own array", hint, back)
		}
		scratch.ReleaseAppendWriter(w)
		scratch.ReleaseBufferSized(buf)
		next := scratch.AcquireBufferSized(hint)
		if next.Len() != 0 || next.Cap() < hint {
			t.Fatalf("hint %d: the class handed back len %d cap %d", hint, next.Len(), next.Cap())
		}
		scratch.ReleaseBufferSized(next)
	}
}

// TestSizedBuffersAndAppendWritersInterleave runs the encode shape both
// primitives serve together, from several goroutines at once and across every
// tier: an encoder writes a record through a pooled AppendWriter into a sized
// buffer's available room, the buffer adopts those bytes, and both go back to
// their pools. Each goroutine writes its own byte, so a buffer or a writer
// handed to two owners at once shows as a foreign byte — and as a race under
// -race.
func TestSizedBuffersAndAppendWritersInterleave(t *testing.T) {
	t.Parallel()
	hints := []int{100, scratch.MaxRetainedBufBytes + 1, 1 << 20, 2 << 20}
	var wg sync.WaitGroup
	for g := range 8 {
		record := bytes.Repeat([]byte{byte('a' + g)}, 512)
		wg.Go(func() {
			for i := range 40 {
				hint := hints[(g+i)%len(hints)]
				buf := scratch.AcquireBufferSized(hint)
				w := scratch.AcquireAppendWriter(buf.AvailableBuffer())
				if _, err := w.Write(record); err != nil {
					t.Errorf("goroutine %d: Write: %v", g, err)
				}
				buf.Write(w.Bytes())
				scratch.ReleaseAppendWriter(w)
				if !bytes.Equal(buf.Bytes(), record) {
					t.Errorf("goroutine %d, hint %d: the buffer holds %d bytes not all its own", g, hint, buf.Len())
				}
				scratch.ReleaseBufferSized(buf)
			}
		})
	}
	wg.Wait()
}
