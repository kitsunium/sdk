package flatbuffers_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/flatbuffers"
)

// FuzzUnmarshal is #897's flatbuffers target: the codec passes buffers through,
// so its parser is validateBuffer — any input is accepted or refused without a
// panic, an accepted buffer is handed over byte for byte, and Marshal accepts
// it back unchanged.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range [][]byte{
		{0x0c, 0x00, 0x00, 0x00, 0x08, 0x00, 0x0c, 0x00, 0x04, 0x00, 0x08, 0x00},
		{0x04, 0x00, 0x00, 0x00},
		{0x01, 0x00, 0x00, 0x80, '{', '}'},
		{0xff, 0xff, 0xff, 0xff},
		{},
	} {
		f.Add(seed)
	}
	c := flatbuffers.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var got []byte
		//: accepted, or refused — never a panic.
		if err := c.Unmarshal(data, &got); err != nil {
			return
		}
		//: the buffer itself.
		if !bytes.Equal(got, data) {
			t.Fatalf("Unmarshal handed over % x, want % x", got, data)
		}
		out, err := c.Marshal(got)
		//: and Marshal takes it back as it is.
		if err != nil || !bytes.Equal(out, data) {
			t.Fatalf("Marshal of an accepted buffer = % x, %v", out, err)
		}
	})
}
