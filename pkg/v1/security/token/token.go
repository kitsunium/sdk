package token

import (
	"encoding/json"
)

// PrivateClaim decodes the named application claim of claims into T.
//
// It lives here rather than on the Claims value because "what shape is a scope
// claim" is the caller's question, not the domain's: the core value stores
// private claims as the exact JSON bytes the issuer signed, so re-encoding a
// decoded value can never change what was authenticated.
//
// The second result reports presence, which is how a claim explicitly set to
// JSON null is told apart from one that was never sent.
func PrivateClaim[T any](claims Claims, name string) (value T, found bool, err error) {
	raw, present := claims.PrivateRaw(name)
	//: absence is a normal answer, not an error.
	if !present {
		//: the zero T, and a false that says why.
		return value, false, nil
	}
	//: a decode failure is the caller's type disagreeing with the token.
	if uerr := json.Unmarshal(raw, &value); uerr != nil {
		//: hand back the stdlib cause; the claim was present.
		return value, true, uerr
	}
	//: decoded.
	return value, true, nil
}

// SetPrivateClaim returns a copy of claims carrying name encoded as JSON. It
// refuses an empty name, one of the seven registered names, and a claim set
// already at its private-claim cap.
func SetPrivateClaim[T any](claims Claims, name string, value T) (updated Claims, err error) {
	raw, merr := json.Marshal(value)
	//: a value the encoder cannot render is a caller error.
	if merr != nil {
		//: hand back the stdlib cause.
		return Claims{}, merr
	}
	//: the core value enforces the name and count rules.
	return claims.WithPrivateRaw(name, raw)
}
