package encwrite

import (
	"encoding/binary"
	"testing"
)

// runFrameCase drives one frame scenario over a byte slice of length n.
func runFrameCase(t *testing.T, n int, wantErr bool) {
	t.Helper()
	box := make([]byte, n)
	//: fill with a recognizable pattern to detect copy errors.
	for i := range box {
		//: each byte mirrors its index modulo 256.
		box[i] = byte(i)
	}
	out, err := frame(box)
	//: error presence must match expectation for this case.
	if (err != nil) != wantErr {
		t.Fatalf("frame(%d) err=%v wantErr=%v", n, err, wantErr)
	}
	//: an error case stops here — there is no framed output to inspect.
	if wantErr {
		//: nothing further to assert on the failure path.
		return
	}
	//: the prefix must carry the box length big-endian.
	got := binary.BigEndian.Uint32(out[:lenPrefixBytes])
	//: the decoded prefix must equal the input length.
	if int(got) != n {
		t.Fatalf("prefix=%d want=%d", got, n)
	}
	//: framed total length must be prefix + payload.
	if len(out) != lenPrefixBytes+n {
		t.Fatalf("len=%d want=%d", len(out), lenPrefixBytes+n)
	}
}

func Test_frame(t *testing.T) {
	t.Parallel()
	//: table of box sizes to frame, including the empty boundary.
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"empty box", 0, false},
		{"small box", 16, false},
		{"larger box", 1024, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFrameCase(t, tc.n, tc.wantErr)
		})
	}
}
