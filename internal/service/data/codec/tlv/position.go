package tlv

import (
	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// privateTrailing is the log text of a refusal of bytes left after the
// first record: placeTyped recognises that refusal by it.
const privateTrailing string = "service/data/codec/tlv.Unmarshal: trailing bytes after the first record"

// placed returns err placed at byte offset off of the input, as the family's
// field codec.PositionOf reads (#1125); its code, reason and texts are err's.
func placed(err error, off int) error {
	//: a field-only wrap: origin wins, the trail is unchanged.
	return errs.Wrap(err, errs.WrapParams{}, codec.PositionFields(codec.PositionValue{Offset: off})...)
}

// placeTyped places a refusal of the typed path. That path does not return
// where it stopped, so the wire is walked again — on a refusal only — and the
// refusal is placed where the walk stops on the same code: the wire fault
// both paths found. A refusal the walk does not reproduce — a value the
// target cannot hold, a well-formed wire — is returned as it is.
func placeTyped(data []byte, err error) error {
	code, ok := errs.CodeOf(err)
	//: success, or an error no wire fault explains.
	if err == nil || !ok {
		return err
	}
	_, rest, werr := decodeValue(data, 0)
	walked, wok := errs.CodeOf(werr)
	//: the walk stopped on the same fault: its residual is where.
	if werr != nil && wok && walked == code {
		return placed(err, cap(data)-cap(rest))
	}
	//: the wire is whole but longer than one record: the first trailing byte.
	if werr == nil && len(rest) != 0 && errs.PrivateOf(err) == privateTrailing {
		return placed(err, len(data)-len(rest))
	}
	//: not a wire fault the walk can place.
	return err
}
