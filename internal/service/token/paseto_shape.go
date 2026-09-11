// Package token — the PASETO claim-value encoding.
package token

import (
	"encoding/json"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pasetoShape is the PASETO claim encoding: RFC 3339 timestamps and a single
// string audience. PASETO's registered claims are typed, and its "aud" is a
// string — there is no array form to accept.
type pasetoShape struct{}

// decodeTime reads an RFC 3339 timestamp.
func (pasetoShape) decodeTime(raw json.RawMessage) (instant time.Time, err error) {
	var text string
	//: PASETO timestamps are JSON strings, not numbers.
	if json.Unmarshal(raw, &text) != nil {
		//: name the shape, never the value.
		return time.Time{}, malformed("PASETO time claim is not a JSON string")
	}
	parsed, perr := time.Parse(time.RFC3339, text)
	//: a non-RFC-3339 timestamp is malformed.
	if perr != nil {
		//: time.ParseError quotes the offending value, which here is a claim
		//: out of an unauthenticated token — so it is not carried.
		return time.Time{}, malformed("PASETO time claim is not RFC 3339")
	}
	//: UTC so comparisons never depend on the sender's zone.
	return parsed.UTC(), nil
}

// encodeTime renders an RFC 3339 timestamp in UTC.
func (pasetoShape) encodeTime(instant time.Time) json.RawMessage {
	//: a hand-rendered literal, so there is no error to discard.
	return json.RawMessage(quoteJSONString(instant.UTC().Format(time.RFC3339)))
}

// decodeAudience accepts a single string only.
func (pasetoShape) decodeAudience(raw json.RawMessage) (audience []string, err error) {
	//: absence is normal.
	if len(raw) == 0 {
		//: no audience claim.
		return nil, nil
	}
	var single string
	//: PASETO defines "aud" as a string; an array is a JWT habit.
	if json.Unmarshal(raw, &single) != nil {
		//: refuse rather than accept a JWT-shaped aud in a PASETO token.
		return nil, malformed("PASETO aud is not a string")
	}
	//: exactly one audience.
	return []string{single}, nil
}

// encodeAudience refuses to render more than one audience: PASETO has no array
// form, and silently emitting only the first would mint a token whose audience
// check passes for a recipient the caller did not intend to include.
func (pasetoShape) encodeAudience(audience []string) (raw json.RawMessage, err error) {
	//: no audience, no member.
	if len(audience) == 0 {
		//: omit.
		return nil, nil
	}
	//: more than one is unrepresentable, so it is refused, not truncated.
	if len(audience) > 1 {
		//: name the constraint, never the audiences.
		return nil, errs.Wrap(coretoken.IssueFailed, errs.WrapParams{},
			errs.String("claim", "PASETO aud accepts exactly one value"))
	}
	//: a hand-rendered literal, so there is no error to discard.
	return json.RawMessage(quoteJSONString(audience[0])), nil
}
