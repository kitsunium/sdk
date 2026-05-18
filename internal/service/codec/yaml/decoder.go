// Package yaml — adapts yaml.v3's *Decoder to codec.Decoder.
package yaml

import (
	"errors"
	"io"

	goyaml "gopkg.in/yaml.v3"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// yamlDecoder wraps *yaml.Decoder so it satisfies codec.Decoder.
type yamlDecoder struct {
	inner *goyaml.Decoder
	//: sticky EOF flag so More() returns false after the stream drains.
	done bool
}

// Decode reads the next document into v.
func (d *yamlDecoder) Decode(v any) error {
	//: stream-end sentinel bubbles up verbatim so callers can exit loops.
	derr := d.inner.Decode(v)
	//: EOF latch toggles the done flag for subsequent More() calls.
	if errors.Is(derr, io.EOF) {
		//: remember that we reached the stream end.
		d.done = true
		//: return io.EOF untouched (matches stdlib decoder semantics).
		return io.EOF
	}
	//: success fast-path.
	if derr == nil {
		//: nothing to wrap.
		return nil
	}
	//: mark drained so More() stops a dec.More()/Decode() loop on error.
	d.done = true
	//: wrap the library error for reason-based matching.
	return errs.Wrap(derr, errs.WrapParams{
		Code:    CodeYAMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "YAML decoding failed",
		Private: "service/codec/yaml.Decoder.Decode: gopkg.in/yaml.v3 returned an error",
	})
}

// More reports whether additional documents are still available.
func (d *yamlDecoder) More() bool {
	//: reflect the sticky EOF latch.
	return !d.done
}
