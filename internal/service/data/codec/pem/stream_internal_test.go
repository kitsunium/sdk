package pem

import (
	"bytes"
	stdpem "encoding/pem"
	"testing"
	"testing/iotest"
)

// FuzzStreamFindsWhatADecodeLoopFinds holds the stream decoder to its
// definition: the blocks it hands out, read one byte at a time, are the
// blocks a loop of encoding/pem.Decode finds over the whole input, in order.
func FuzzStreamFindsWhatADecodeLoopFinds(f *testing.F) {
	two := append(stdpem.EncodeToMemory(&stdpem.Block{Type: "A", Bytes: []byte("one")}), stdpem.EncodeToMemory(&stdpem.Block{Type: "B", Headers: map[string]string{"k": "v"}, Bytes: []byte("two")})...)
	f.Add(two)
	f.Add(append([]byte("junk\n-----BEGIN X-----\nnot base64\n-----END X-----\n"), two...))
	f.Add([]byte("-----BEGIN A-----\n-----BEGIN B-----\nAAAA\n-----END B-----\n"))
	f.Add([]byte("-----BEGIN A-----\r\nAAAA\r\n-----END A-----"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var want []*stdpem.Block
		for rest := data; ; {
			var b *stdpem.Block
			b, rest = stdpem.Decode(rest)
			if b == nil {
				break
			}
			want = append(want, b)
		}
		dec := Codec.(*pemCodec).NewDecoder(iotest.OneByteReader(bytes.NewReader(data)))
		var got []*stdpem.Block
		for dec.More() {
			var b *stdpem.Block
			if err := dec.Decode(&b); err != nil {
				break
			}
			got = append(got, b)
		}
		if len(got) != len(want) {
			t.Fatalf("stream found %d blocks, a pem.Decode loop %d", len(got), len(want))
		}
		for i := range want {
			if got[i].Type != want[i].Type || !bytes.Equal(got[i].Bytes, want[i].Bytes) || len(got[i].Headers) != len(want[i].Headers) {
				t.Fatalf("block %d: stream %+v, loop %+v", i, got[i], want[i])
			}
		}
	})
}

// TestStreamWaitsForTheLastEndLine pins a block whose END line is not whole
// yet, after an END line that decoded nothing: it is not decided until its
// line is, since the rest of the line can still refuse it.
func TestStreamWaitsForTheLastEndLine(t *testing.T) {
	t.Parallel()
	input := "-----BEGIN X-----\n!!\n-----END X-----\n-----BEGIN A-----\nAAAA\n-----END A----- junk\n"
	var want []*stdpem.Block
	for rest := []byte(input); ; {
		var b *stdpem.Block
		if b, rest = stdpem.Decode(rest); b == nil {
			break
		}
		want = append(want, b)
	}
	dec := Codec.(*pemCodec).NewDecoder(iotest.OneByteReader(bytes.NewReader([]byte(input))))
	n := 0
	for dec.More() {
		var b *stdpem.Block
		if err := dec.Decode(&b); err != nil {
			break
		}
		n++
	}
	if n != len(want) {
		t.Fatalf("stream found %d blocks, a pem.Decode loop %d", n, len(want))
	}
}
