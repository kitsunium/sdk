package xml_test

import (
	"bytes"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/xml"
)

// appender is the codec under test as a codec.Appender.
func appender(t *testing.T) corecodec.Appender {
	t.Helper()
	a, ok := xml.New().(corecodec.Appender)
	if !ok {
		t.Fatal("the xml codec does not implement codec.Appender")
	}
	return a
}

// TestAppendWritesMarshalsBytes pins #1124's wire contract: Append writes
// Marshal's bytes, byte for byte, after the caller's prefix — into dst's own
// array when it has room, into a grown one when it has not.
func TestAppendWritesMarshalsBytes(t *testing.T) {
	t.Parallel()
	a := appender(t)
	for name, v := range appendFixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want, err := a.Marshal(v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, capacity := range []int{0, 3, 3 + len(want), 4096} {
				dst := append(make([]byte, 0, capacity), "PRE"...)
				got, aerr := a.Append(dst, v)
				if aerr != nil {
					t.Fatalf("cap %d: Append: %v", capacity, aerr)
				}
				if !bytes.Equal(got[:3], []byte("PRE")) || !bytes.Equal(got[3:], want) {
					t.Fatalf("cap %d: Append wrote %q, want %q after the prefix", capacity, got, want)
				}
				if fits := cap(dst) >= len(got); fits != (&got[0] == &dst[0]) {
					t.Errorf("cap %d: Append used dst's array = %v, want %v", capacity, !fits, fits)
				}
			}
		})
	}
}

// TestAppendLeavesDstOnFailure is the Appender contract on failure: the
// returned slice is dst, at its original length and with its contents.
func TestAppendLeavesDstOnFailure(t *testing.T) {
	t.Parallel()
	a := appender(t)
	for _, capacity := range []int{3, 64, 1 << 16} {
		dst := append(make([]byte, 0, capacity), "PRE"...)
		got, err := a.Append(dst, appendFailure())
		if err == nil {
			t.Fatalf("cap %d: Append of a value Marshal refuses succeeded", capacity)
		}
		if string(got) != "PRE" {
			t.Fatalf("cap %d: Append returned %q on failure, want dst %q", capacity, got, "PRE")
		}
	}
}

// TestAppendAliasesNoPooledBuffer pins that what Append returns is the
// caller's alone: scribbling over it and encoding again changes nothing.
func TestAppendAliasesNoPooledBuffer(t *testing.T) {
	t.Parallel()
	a := appender(t)
	for name, v := range appendFixtures() {
		want, err := a.Marshal(v)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		first, err := a.Append(nil, v)
		if err != nil {
			t.Fatalf("%s: Append: %v", name, err)
		}
		for i := range first {
			first[i] = '#'
		}
		again, err := a.Append(nil, v)
		if err != nil {
			t.Fatalf("%s: Append: %v", name, err)
		}
		if !bytes.Equal(again, want) {
			t.Fatalf("%s: a second Append wrote %q after the first result was overwritten, want %q", name, again, want)
		}
		if remarshal, _ := a.Marshal(v); !bytes.Equal(remarshal, want) {
			t.Fatalf("%s: Marshal after an overwritten Append wrote %q", name, remarshal)
		}
	}
}
