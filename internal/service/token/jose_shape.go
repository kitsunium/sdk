// Package token — the JWT/JOSE claim-value encoding.
package token

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxNumericDate bounds a JWT NumericDate, in seconds since the epoch. It is
// roughly the year 5138 — far past any legitimate expiry, and far short of the
// values that make time.Unix overflow into a negative instant. A bound is
// needed because "exp": 1e300 is valid JSON, and an implementation that lets
// it through has a token that is not expired for reasons of floating point.
const maxNumericDate float64 = 1e11

// joseShape is the JWT/JOSE claim encoding: NumericDate seconds
// (RFC 7519 §2), and an "aud" that is either one string or an array of them
// (RFC 7519 §4.1.3).
type joseShape struct{}

// decodeTime reads a NumericDate. It accepts a fractional value because the
// RFC defines one, and keeps it to nanosecond resolution.
func (joseShape) decodeTime(raw json.RawMessage) (instant time.Time, err error) {
	var seconds float64
	//: a NumericDate is a JSON number; a string here is a PASETO habit.
	if json.Unmarshal(raw, &seconds) != nil {
		//: name the shape, never the value.
		return time.Time{}, malformed("NumericDate claim is not a JSON number")
	}
	//: NaN and infinities compare false against everything, so a token
	//: carrying one would be neither expired nor valid. Refuse it instead.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) > maxNumericDate {
		//: out-of-range is malformed, not "very far in the future".
		return time.Time{}, errs.Wrap(coretoken.Malformed, errs.WrapParams{},
			errs.String("claim", "NumericDate out of range"))
	}
	whole, frac := math.Modf(seconds)
	//: seconds + nanoseconds, in UTC so comparisons never depend on a zone.
	return time.Unix(int64(whole), int64(frac*float64(time.Second))).UTC(), nil
}

// encodeTime renders a NumericDate as whole seconds.
func (joseShape) encodeTime(instant time.Time) json.RawMessage {
	//: integer seconds — the spelling every JWT reader accepts.
	return json.RawMessage(strconv.FormatInt(instant.Unix(), decimalBase))
}

// decodeAudience accepts the one-string and the array-of-strings forms, and
// nothing else. An empty array decodes to no audience at all.
func (joseShape) decodeAudience(raw json.RawMessage) (audience []string, err error) {
	//: absence is normal — "aud" is OPTIONAL.
	if len(raw) == 0 {
		//: no audience claim.
		return nil, nil
	}
	var single string
	//: the common single-recipient spelling.
	if json.Unmarshal(raw, &single) == nil {
		//: one audience.
		return []string{single}, nil
	}
	var many []string
	//: the multi-recipient spelling.
	if json.Unmarshal(raw, &many) != nil {
		//: anything else — a number, an object, a mixed array — is malformed.
		return nil, malformed("aud is neither a string nor an array of strings")
	}
	//: the array as sent, order preserved.
	return many, nil
}

// encodeAudience renders one audience as a string and several as an array,
// which is the shape RFC 7519 §4.1.3 recommends for the single case.
func (joseShape) encodeAudience(audience []string) (raw json.RawMessage, err error) {
	//: no audience, no member.
	if len(audience) == 0 {
		//: omit.
		return nil, nil
	}
	//: the single-recipient special case, per the RFC's own note.
	if len(audience) == 1 {
		//: a hand-rendered literal, so there is no error to discard.
		return json.RawMessage(quoteJSONString(audience[0])), nil
	}
	//: the array form, likewise rendered rather than reflected.
	return json.RawMessage(quoteJSONStrings(audience)), nil
}
