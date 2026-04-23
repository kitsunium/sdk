package codec_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
)

// fakeAppendCodec is a minimal Appender implementation used to exercise the
// type-assertion contract callers rely on (Codec ↛ Appender, Codec → Appender).
type fakeAppendCodec struct {
	suffix []byte
	err    error
}

func (f *fakeAppendCodec) Name() (name string)         { return "fake" }
func (f *fakeAppendCodec) MIMETypes() (mimes []string) { return []string{"x/fake"} }
func (f *fakeAppendCodec) Extensions() (exts []string) { return []string{".fake"} }
func (f *fakeAppendCodec) Marshal(_ any) (data []byte, err error) {
	//: Marshal is not exercised by this test — the Appender path is the focus.
	return nil, f.err
}
func (f *fakeAppendCodec) Unmarshal(_ []byte, _ any) (err error) {
	//: Unmarshal is not exercised by this test — the Appender path is the focus.
	return f.err
}
func (f *fakeAppendCodec) Append(dst []byte, _ any) (out []byte, err error) {
	//: shortcut error path so callers can exercise the failure branch.
	if f.err != nil {
		//: surface the canned error so consumers can errors.Is against it.
		return dst, f.err
	}
	//: append the canned suffix verbatim — the simplest deterministic encoding.
	return append(dst, f.suffix...), nil
}

// TestAppenderTypeAssertion confirms that consumers can detect Appender
// support on a Codec via a comma-ok type assertion.
func TestAppenderTypeAssertion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		c         codec.Codec
		wantOk    bool
		wantBytes []byte
	}{
		{"Appender impl satisfies the interface", &fakeAppendCodec{suffix: []byte("ok")}, true, []byte("ok")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, ok := tc.c.(codec.Appender)
			if ok != tc.wantOk {
				t.Errorf("type assertion ok = %v, want %v", ok, tc.wantOk)
			}
			if !ok {
				return
			}
			got, err := a.Append(nil, struct{}{})
			if err != nil {
				t.Errorf("Append err = %v", err)
			}
			if string(got) != string(tc.wantBytes) {
				t.Errorf("Append bytes = %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

// TestAppenderErrorPropagation verifies the codec's error is surfaced
// verbatim through the Append return value.
func TestAppenderErrorPropagation(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{"canned error round-trips through Append", boom, boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &fakeAppendCodec{err: tc.err}
			_, got := c.Append(nil, nil)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("errors.Is(err, want) = false: %v", got)
			}
		})
	}
}
