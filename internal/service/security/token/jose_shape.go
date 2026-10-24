package token

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxNumericDate bounds a JWT NumericDate, in seconds since the epoch. It is
// roughly the year 5138 — far past any legitimate expiry, and far short of the
// values that make time.Unix overflow into a negative instant. A bound is
// needed because "exp": 1e300 is valid JSON, and an implementation that lets
// it through has a token that is not expired for reasons of floating point.
const maxNumericDate float64 = 1e11

// maxIntegerDigits is the longest integer NumericDate read without strconv:
// eighteen digits never overflow an int64, and every one of them past twelve
// is out of range anyway.
const maxIntegerDigits int = 18

// joseShape is the JWT/JOSE claim encoding: NumericDate seconds
// (RFC 7519 §2), and an "aud" that is either one string or an array of them
// (RFC 7519 §4.1.3).
type joseShape struct{}

// decodeTime reads a NumericDate. It accepts a fractional value because the
// RFC defines one, and keeps it to nanosecond resolution.
//
// An integer of up to maxIntegerDigits digits — every real exp, nbf and iat —
// is read exactly, without strconv and without an allocation; anything else
// goes through strconv.ParseFloat, once the scanner has held it to the RFC
// 8259 grammar, which ParseFloat alone is not ("0x1p3", "Inf", "1_0").
func (joseShape) decodeTime(raw []byte) (instant time.Time, err error) {
	//: a NumericDate is a JSON number; a string here is a PASETO habit.
	if len(raw) == 0 || (raw[0] != '-' && !isDigit(raw[0])) {
		//: name the shape, never the value.
		return time.Time{}, malformed("NumericDate claim is not a JSON number")
	}
	//: the exact path: an integer, read as one.
	if whole, ok := numericInteger(raw); ok {
		//: the same bound the fractional path applies.
		if math.Abs(float64(whole)) > maxNumericDate {
			//: out-of-range is malformed, not "very far in the future".
			return time.Time{}, numericDateOutOfRange()
		}
		//: whole seconds, in UTC so comparisons never depend on a zone.
		return time.Unix(whole, 0).UTC(), nil
	}
	seconds, perr := strconv.ParseFloat(string(raw), 64)
	//: a range error is as unreadable as a syntax error.
	if perr != nil {
		//: name the shape, never the value.
		return time.Time{}, malformed("NumericDate claim is not a JSON number")
	}
	//: NaN and infinities compare false against everything, so a token
	//: carrying one would be neither expired nor valid. Refuse it instead.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) > maxNumericDate {
		//: out-of-range is malformed, not "very far in the future".
		return time.Time{}, numericDateOutOfRange()
	}
	whole, frac := math.Modf(seconds)
	//: seconds + nanoseconds, in UTC so comparisons never depend on a zone.
	return time.Unix(int64(whole), int64(frac*float64(time.Second))).UTC(), nil
}

// numericInteger reads raw, a number the scanner validated, when it is an
// integer of at most maxIntegerDigits digits; ok is false otherwise, and the
// caller takes the general path.
func numericInteger(raw []byte) (value int64, ok bool) {
	digits := raw
	negative := raw[0] == '-'
	//: the sign is not a digit.
	if negative {
		digits = raw[1:]
	}
	//: past this many digits the int64 could overflow; such a number is out
	//: of range anyway, and the general path says so.
	if len(digits) == 0 || len(digits) > maxIntegerDigits {
		//: the general path.
		return 0, false
	}
	//: most significant digit first.
	for _, c := range digits {
		//: a fraction or an exponent is not an integer.
		if !isDigit(c) {
			//: the general path.
			return 0, false
		}
		value = value*int64(decimalBase) + int64(c-'0')
	}
	//: the sign, last.
	if negative {
		value = -value
	}
	//: an exact integer.
	return value, true
}

// numericDateOutOfRange returns the verdict for a NumericDate past the bound.
func numericDateOutOfRange() error {
	//: one detail for every path that reaches it.
	return errs.Wrap(coretoken.Malformed, errs.WrapParams{},
		errs.String("claim", "NumericDate out of range"))
}

// encodeTime renders a NumericDate as whole seconds.
func (joseShape) encodeTime(instant time.Time) json.RawMessage {
	//: integer seconds — the spelling every JWT reader accepts.
	return json.RawMessage(strconv.FormatInt(instant.Unix(), decimalBase))
}

// checkAudience accepts the one-string and the array-of-strings forms, and
// nothing else — a null member of the array included, which a lenient reader
// decodes as "".
func (joseShape) checkAudience(raw []byte) error {
	//: the first byte names the form.
	switch raw[0] {
	//: the common single-recipient spelling.
	case '"':
		//: one audience.
		return nil
	//: the multi-recipient spelling: every member a string.
	case '[':
		walk := newArrayStrings(raw)
		//: member by member, without building anything.
		for {
			_, more, err := walk.next()
			//: a member that is not a string.
			if err != nil {
				//: propagate Malformed.
				return err
			}
			//: the end of the array.
			if !more {
				//: every member a string.
				return nil
			}
		}
	//: a number, an object, a literal.
	default:
		//: malformed.
		return malformed("aud is neither a string nor an array of strings")
	}
}

// decodeAudience reads an "aud" checkAudience accepted. An empty array decodes
// to no audience at all.
func (joseShape) decodeAudience(raw []byte) (audience []string, err error) {
	//: absence is normal — "aud" is OPTIONAL.
	if len(raw) == 0 {
		//: no audience claim.
		return nil, nil
	}
	//: the common single-recipient spelling.
	if raw[0] == '"' {
		//: one audience.
		return []string{decodeText(raw)}, nil
	}
	//: non-nil even when empty, as encoding/json decoded "[]"; sized for the
	//: audiences a ClaimsBuf holds, and grown past them.
	audience = make([]string, 0, coretoken.ClaimsBufAudienceCap)
	walk := newArrayStrings(raw)
	//: the array as sent, order preserved.
	for {
		lit, more, werr := walk.next()
		//: checkAudience already refused a non-string; kept typed anyway.
		if werr != nil {
			//: propagate.
			return nil, werr
		}
		//: the end of the array.
		if !more {
			//: every member.
			return audience, nil
		}
		audience = append(audience, decodeText(lit))
	}
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
