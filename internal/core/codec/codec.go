// Package codec declares the domain contracts every format codec in the SDK
// implements. Consumers interact with codecs through the pkg/v1/codec facade;
// concrete implementations live in internal/service/codec/<format>/.
//
// This package is interface-only: no runtime state, no registrations, no
// format-specific knowledge. Codecs register themselves via the registry in
// registry.go when their service subpackage is imported.
package codec

import "io"

// Codec is the minimum contract every format codec satisfies. Instances MUST
// be safe for concurrent use; format-specific options are supplied via the
// codec's constructor, not via the Codec interface.
type Codec interface {
	Name() (name string)
	MIMETypes() (mimes []string)
	Extensions() (exts []string)
	Marshal(v any) (data []byte, err error)
	Unmarshal(data []byte, v any) (err error)
}

// StreamingCodec is the optional extension for formats that support
// incremental encode/decode over io.Writer / io.Reader. Codecs that CAN
// stream SHOULD implement it; consumers detect support with a type assertion.
type StreamingCodec interface {
	Codec
	NewEncoder(w io.Writer) (enc Encoder)
	NewDecoder(r io.Reader) (dec Decoder)
}

// Encoder writes one or more values to the wrapped writer.
type Encoder interface {
	Encode(v any) (err error)
	Close() (err error)
}

// Decoder reads one or more values from the wrapped reader.
type Decoder interface {
	Decode(v any) (err error)
	More() (ok bool)
}
