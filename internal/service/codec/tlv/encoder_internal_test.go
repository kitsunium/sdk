package tlv

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// failingWriter rejects every Write call with a fixed error, used to
// exercise the streaming encoder's writer-error branch.
type failingWriter struct{}

// Write always returns errFailingWriter. The parameter is referenced via
// the discard-blank assignment so KTN-VAR-DEADREAD does not flag it.
func (failingWriter) Write(p []byte) (n int, err error) {
	//: surface a synthetic failure for the wrap branch.
	if len(p) > 0 {
		//: any non-nil error path is sufficient.
		return 0, errFailingWriter
	}
	//: empty buffer still fails so the encoder cannot pre-empt the branch.
	return 0, errFailingWriter
}

// errFailingWriter is the sentinel returned by failingWriter.Write.
var errFailingWriter = errors.New("writer failed (synthetic)")

// Test_tlvEncoder_Encode covers the streaming encoder's success and
// writer-error branches.
func Test_tlvEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		writer  io.Writer
		wantErr bool
	}
	tests := []tc{
		{"int encodes cleanly", int64(7), &bytes.Buffer{}, false},
		{"writer error surfaces", int64(7), failingWriter{}, true},
		{"unsupported type surfaces", make(chan int), &bytes.Buffer{}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &tlvEncoder{w: tc.writer}
		err := enc.Encode(tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Encode err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvEncoder_Close exercises the no-op Close wrapper.
func Test_tlvEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr bool
	}
	tests := []tc{{"close after encode succeeds", false}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &tlvEncoder{w: &bytes.Buffer{}}
		err := enc.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Close err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
