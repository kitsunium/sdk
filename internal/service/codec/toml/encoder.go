// Package toml — adapts pelletier's *Encoder to codec.Encoder.
package toml

import (
	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tomlEncoder wraps *gotoml.Encoder so it satisfies codec.Encoder.
type tomlEncoder struct {
	inner *gotoml.Encoder
}

// Encode serialises v through the wrapped encoder.
func (e *tomlEncoder) Encode(v any) error {
	//: delegate and wrap on error.
	terr := e.inner.Encode(v)
	//: success fast-path.
	if terr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(terr, errs.WrapParams{
		Code:    CodeTOMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "TOML encoding failed",
		Private: "service/codec/toml.Encoder.Encode: pelletier/go-toml/v2 returned an error",
	})
}

// Close is a no-op because the pelletier encoder does not own the writer.
func (*tomlEncoder) Close() error {
	//: pelletier's encoder owns no writer-level state.
	return nil
}
