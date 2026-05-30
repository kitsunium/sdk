package streamaead

import (
	"bytes"
	"errors"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// failWriter fails every Write to drive the writer's sink-fault paths.
type failWriter struct{}

func (failWriter) Write(_ []byte) (int, error) {
	//: a sentinel sink fault for the writer error paths.
	return 0, errors.New("sink down")
}

func wKey(t *testing.T) corecrypto.Key {
	t.Helper()
	//: deterministic 32-byte key.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{0x1}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

func newTestWriter(t *testing.T, dst io.Writer) *streamWriter {
	t.Helper()
	var salt [saltLen]byte
	//: build the writer through the test-only deterministic-salt seam.
	w, err := newWriterWithSalt(wKey(t), dst, nil, salt)
	if err != nil {
		t.Fatalf("newWriterWithSalt: %v", err)
	}
	return w.(*streamWriter)
}

func Test_newWriterWithSalt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"constructs a writer with a derived GCM mode"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var salt [saltLen]byte
			//: a well-formed key + salt must construct a non-nil writer.
			w, err := newWriterWithSalt(wKey(t), &bytes.Buffer{}, nil, salt)
			if err != nil || w == nil {
				t.Fatalf("newWriterWithSalt=(%v,%v) want (writer,nil)", w, err)
			}
		})
	}
}

func Test_streamWriter_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dst     io.Writer
		wantErr bool
	}{
		{"a healthy sink accepts the write", &bytes.Buffer{}, false},
		{"a failing sink surfaces the header fault", failWriter{}, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newTestWriter(t, c.dst)
			//: Write emits the header lazily, so a failing sink errors here.
			_, err := w.Write([]byte("hi"))
			if (err != nil) != c.wantErr {
				t.Errorf("Write err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func Test_streamWriter_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dst     io.Writer
		wantErr bool
	}{
		{"a healthy sink seals the final chunk", &bytes.Buffer{}, false},
		{"a failing sink surfaces the header fault", failWriter{}, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newTestWriter(t, c.dst)
			//: first Close seals the final chunk (or surfaces the sink fault).
			err := w.Close()
			if (err != nil) != c.wantErr {
				t.Fatalf("Close err=%v wantErr=%v", err, c.wantErr)
			}
			//: a second Close is always an idempotent no-op.
			if serr := w.Close(); serr != nil {
				t.Errorf("second Close: %v want nil", serr)
			}
			//: a write after Close is always rejected.
			if _, werr := w.Write([]byte("x")); werr == nil {
				t.Errorf("Write after Close succeeded")
			}
		})
	}
}

func Test_streamWriter_ensureHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dst     io.Writer
		wantErr bool
	}{
		{"a healthy sink emits the header once", &bytes.Buffer{}, false},
		{"a failing sink surfaces the fault", failWriter{}, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newTestWriter(t, c.dst)
			//: first call emits the fixed header (or surfaces the sink fault).
			if err := w.ensureHeader(); (err != nil) != c.wantErr {
				t.Fatalf("ensureHeader err=%v wantErr=%v", err, c.wantErr)
			}
			//: a second call is always a no-op, even after a fault.
			if err := w.ensureHeader(); (err != nil) != c.wantErr {
				t.Errorf("ensureHeader twice err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func Test_streamWriter_sealChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dst     io.Writer
		wantErr bool
	}{
		{"a healthy sink seals ct||tag", &bytes.Buffer{}, false},
		{"a failing sink surfaces the fault", failWriter{}, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newTestWriter(t, c.dst)
			w.buf = append(w.buf, []byte("payload")...)
			//: sealing the buffered chunk advances the counter or surfaces a fault.
			err := w.sealChunk(flagFinal)
			if (err != nil) != c.wantErr {
				t.Fatalf("sealChunk err=%v wantErr=%v", err, c.wantErr)
			}
			//: a healthy seal advances the counter and clears the buffer.
			if !c.wantErr && (w.counter != 1 || len(w.buf) != 0) {
				t.Errorf("after sealChunk counter=%d bufLen=%d want 1,0", w.counter, len(w.buf))
			}
		})
	}
}
