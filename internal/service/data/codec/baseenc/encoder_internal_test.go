package baseenc

import (
	"bytes"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errEncSink always fails its Write, used to force the streaming encoder's
// JSON-encode (hex) and base-N Close-flush (base64) error arms. base64
// buffers a tiny payload until Close, so the failure surfaces from Close;
// hex writes straight through, so the failure surfaces from Encode.
type errEncSink struct{}

// Write always rejects the buffer so the encode pipeline cannot complete.
func (errEncSink) Write(p []byte) (n int, err error) {
	//: any non-nil error path is sufficient to drive the wrap branch.
	return 0, errEncSinkFailed
}

// errEncSinkFailed is the sentinel returned by errEncSink.Write.
var errEncSinkFailed = errors.New("encoder sink failed (synthetic)")

// Test_baseencEncoder_Encode drives the streaming encoder over every
// variant so a regression in the json/base-N pipeline wiring fails
// loudly.
func Test_baseencEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 encode", variantBase64},
		{"base64url encode", variantBase64URL},
		{"base32 encode", variantBase32},
		{"base16 encode", variantBase16},
		{"hex encode", variantHex},
		{"ascii85 encode", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		if err := enc.Encode("payload"); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		//: Close to flush so we can sanity-check non-empty output.
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: Encode+Close produced no bytes", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencEncoder_Close_writerError drives the Close error arm. base64
// buffers a tiny payload internally during Encode and flushes the tail on
// Close, so a writer that rejects every Write lets Encode succeed yet trips
// the base-N writer's Close — surfacing CodeBaseEncMarshalFailed.
func Test_baseencEncoder_Close_writerError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 close flush fails", variantBase64},
		{"base64url close flush fails", variantBase64URL},
		{"base32 close flush fails", variantBase32},
		{"ascii85 close flush fails", variantASCII85},
		{"base16 close flush fails", variantBase16},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		enc := c.NewEncoder(errEncSink{})
		//: a 1-byte JSON value stays inside the base-N encoder's internal
		//: buffer, so Encode succeeds and the rejected flush only lands on
		//: Close where the wrap branch lives.
		if err := enc.Encode(1); err != nil {
			t.Fatalf("%s: Encode err=%v (expected buffered success)", tc.name, err)
		}
		err := enc.Close()
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
		//: the synthetic sink error must survive the wrap.
		if !errors.Is(err, errEncSinkFailed) {
			t.Errorf("%s: expected errEncSinkFailed in chain, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencEncoder_Encode_jsonRejected drives the Encode error arm: a
// chan value is non-encodable by encoding/json so the wrap branch surfaces
// CodeBaseEncMarshalFailed regardless of the underlying writer.
func Test_baseencEncoder_Encode_jsonRejected(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 rejects chan", variantBase64},
		{"ascii85 rejects chan", variantASCII85},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		//: chan int is the canonical non-encodable value; json.Encode trips
		//: before any base-N byte reaches the sink.
		err := enc.Encode(make(chan int))
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencEncoder_Encode_writerError drives the Encode error arm via a
// writer that rejects every Write. The hex variant streams JSON bytes
// straight through with no internal buffering, so the failure surfaces
// from Encode rather than Close.
func Test_baseencEncoder_Encode_writerError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{{"hex surfaces writer error from Encode", variantHex}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		enc := c.NewEncoder(errEncSink{})
		err := enc.Encode(1)
		if !errs.HasCode(err, CodeBaseEncMarshalFailed) {
			t.Errorf("%s: expected CodeBaseEncMarshalFailed, got %v", tc.name, err)
		}
		//: the synthetic sink error must survive the wrap so errors.Is keeps
		//: working for callers matching the underlying cause.
		if !errors.Is(err, errEncSinkFailed) {
			t.Errorf("%s: expected errEncSinkFailed in chain, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_baseencEncoder_Close verifies the encoder's Close finalises the
// underlying base-N writer (emitting any padding / Adobe-frame markers)
// before the caller can drain the buffer.
func Test_baseencEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
	}
	tests := []tc{
		{"base64 close emits", variantBase64},
		{"hex close emits", variantHex},
		{"ascii85 close emits", variantASCII85},
		{"base16 close flushes buffered writer", variantBase16},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &baseencCodec{variant: tc.v}
		var sink bytes.Buffer
		enc := c.NewEncoder(&sink)
		if err := enc.Encode("payload"); err != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, err)
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("%s: Close err=%v", tc.name, err)
		}
		if sink.Len() == 0 {
			t.Errorf("%s: Close did not flush — buffer empty", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
