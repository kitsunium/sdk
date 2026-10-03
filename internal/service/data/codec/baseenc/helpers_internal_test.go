package baseenc

import (
	"bytes"
	"testing"
)

// Test_nopWriteCloser_Close_internal covers the io.WriteCloser shim used
// by variants whose stdlib encoder is a plain io.Writer. The shim must
// honour the wrapped Writer for Write and return nil from Close.
func Test_nopWriteCloser_Close_internal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
	}
	tests := []tc{
		{"empty close", []byte{}},
		{"single byte then close", []byte{0x41}},
		{"multi byte then close", []byte("payload")},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var sink bytes.Buffer
		w := nopWriteCloser{Writer: &sink}
		if _, err := w.Write(tc.payload); err != nil {
			t.Fatalf("%s: Write err=%v", tc.name, err)
		}
		if err := w.Close(); err != nil {
			t.Errorf("%s: Close err=%v want nil", tc.name, err)
		}
		if !bytes.Equal(sink.Bytes(), tc.payload) {
			t.Errorf("%s: sink=%q want %q", tc.name, sink.Bytes(), tc.payload)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
