package codec

import (
	"bytes"
	"testing"
)

// fakeSniffer claims every prefix that opens with its head, and records the
// length of the last prefix it was handed.
type fakeSniffer struct {
	Codec
	head []byte
	seen int
}

// Sniff claims a prefix opening with the head.
func (s *fakeSniffer) Sniff(prefix []byte) bool {
	s.seen = len(prefix)
	return bytes.HasPrefix(prefix, s.head)
}

// TestDetectAnswersOnlyOneClaim pins Detect's rule (#909) over a list of its
// own: one claim is the answer, none or two is no answer, and a sniffer is
// never handed more than 512 bytes. Not parallel: it swaps the process-wide
// list and puts it back.
func TestDetectAnswersOnlyOneClaim(t *testing.T) {
	saved := sniffers.Load()
	t.Cleanup(func() { sniffers.Store(saved) })
	a := &fakeSniffer{head: []byte("ab")}
	b := &fakeSniffer{head: []byte("a")}
	sniffers.Store(&[]sniffEntry{{name: "a", sniffer: a}, {name: "b", sniffer: b}})
	for _, tc := range []struct {
		in   string
		want Format
		ok   bool
	}{
		{"az", "b", true},
		{"ab", "", false},
		{"zz", "", false},
		{"", "", false},
	} {
		//: one claim, or none.
		if got, ok := Detect([]byte(tc.in)); got != tc.want || ok != tc.ok {
			t.Errorf("Detect(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	_, _ = Detect(bytes.Repeat([]byte("z"), 4096))
	//: the window.
	if a.seen != sniffLen || b.seen != sniffLen {
		t.Errorf("sniffers saw %d and %d bytes, want %d", a.seen, b.seen, sniffLen)
	}
	sniffers.Store(nil)
	//: no sniffer at all.
	if got, ok := Detect([]byte("ab")); ok {
		t.Errorf("Detect with no sniffer = %q", got)
	}
}
