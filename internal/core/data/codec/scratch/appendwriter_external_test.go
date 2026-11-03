package scratch_test

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// TestAppendWriterAppendsInPlace pins what an Append built on the writer
// relies on: the caller's prefix stays, every Write lands after it, a write
// that fits the capacity lands in the caller's own array, and one that does
// not grows a new array holding the same prefix.
func TestAppendWriterAppendsInPlace(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		capacity  int
		writes    []string
		sameArray bool
	}
	tests := []tc{
		{"fits the capacity", 64, []string{"ab", "cd", ""}, true},
		{"outgrows the capacity", 4, []string{"abcdef", "ghij"}, false},
		{"nothing written", 8, nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dst := append(make([]byte, 0, tc.capacity), "xy"...)
		w := scratch.AcquireAppendWriter(dst)
		defer scratch.ReleaseAppendWriter(w)
		want := "xy"
		for _, s := range tc.writes {
			n, err := io.WriteString(w, s)
			if err != nil || n != len(s) {
				t.Fatalf("Write(%q) = %d, %v; want %d, nil", s, n, err, len(s))
			}
			want += s
		}
		got := w.Bytes()
		if string(got) != want {
			t.Fatalf("Bytes() = %q, want %q", got, want)
		}
		if same := &got[0] == &dst[0]; same != tc.sameArray {
			t.Errorf("Bytes() shares dst's array = %v, want %v", same, tc.sameArray)
		}
		if string(dst) != "xy" {
			t.Errorf("dst = %q after the writes, want its prefix unchanged", dst)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestAppendWriterTruncateRestoresTheCallersBytes is the Appender contract on
// failure: whatever was written, Truncate hands back the caller's bytes at
// their original length, and a later Write starts again after them.
func TestAppendWriterTruncateRestoresTheCallersBytes(t *testing.T) {
	t.Parallel()
	for _, capacity := range []int{2, 3, 128} {
		t.Run(fmt.Sprintf("cap %d", capacity), func(t *testing.T) {
			t.Parallel()
			dst := append(make([]byte, 0, capacity), "xy"...)
			w := scratch.AcquireAppendWriter(dst)
			defer scratch.ReleaseAppendWriter(w)
			_, _ = w.Write([]byte("a torn encode"))
			if got := w.Truncate(); !bytes.Equal(got, []byte("xy")) || len(got) != len(dst) {
				t.Fatalf("Truncate() = %q, want %q", got, "xy")
			}
			_, _ = w.Write([]byte("!"))
			if got := w.Bytes(); string(got) != "xy!" {
				t.Fatalf("Bytes() after Truncate and Write = %q, want %q", got, "xy!")
			}
		})
	}
}

// TestAppendWriterReleaseForgetsTheSlice pins that a released writer holds no
// caller's buffer: the next acquisition starts from its own dst, and a nil
// release is harmless.
func TestAppendWriterReleaseForgetsTheSlice(t *testing.T) {
	t.Parallel()
	w := scratch.AcquireAppendWriter([]byte("first"))
	_, _ = w.Write([]byte(" caller"))
	scratch.ReleaseAppendWriter(w)
	scratch.ReleaseAppendWriter(nil)
	next := scratch.AcquireAppendWriter(nil)
	defer scratch.ReleaseAppendWriter(next)
	if got := next.Bytes(); len(got) != 0 {
		t.Fatalf("a fresh writer over nil holds %q", got)
	}
	if got := next.Truncate(); len(got) != 0 {
		t.Fatalf("Truncate on a fresh writer = %q", got)
	}
	var zero scratch.AppendWriter
	_, _ = zero.Write([]byte("z"))
	if string(zero.Bytes()) != "z" {
		t.Fatalf("the zero AppendWriter wrote %q", zero.Bytes())
	}
}
