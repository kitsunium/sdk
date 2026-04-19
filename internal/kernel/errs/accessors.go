// Package errs: accessors.go provides package-level introspection helpers
// that walk a cause chain and read fields from the deepest *Error
// encountered. Consumers of pkg/v1/errs use re-exports of these.
package errs

import "errors"

// CodeOf walks the Unwrap chain and returns the deepest *Error.Code.
//
// Params:
//   - err: any error value; the chain is walked via errors.As.
//
// Returns:
//   - int: the deepest *Error.Code, or 0 if no *Error is found.
//   - bool: true iff an *Error was found anywhere in the chain.
func CodeOf(err error) (code int, ok bool) {
	//: walk the chain and keep the deepest *Error we see.
	deepest := deepestError(err)
	//: absence path → zero pair by contract.
	if deepest == nil {
		//: signal "no *Error in the chain" to callers.
		return 0, false
	}
	//: hand back the deepest code.
	return deepest.code, true
}

// ReasonOf walks the Unwrap chain and returns the deepest *Error.Reason.
//
// Params:
//   - err: any error value.
//
// Returns:
//   - string: the Reason, or "" if no *Error is found.
//   - bool: true iff an *Error was found.
func ReasonOf(err error) (reason string, ok bool) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → empty pair.
	if deepest == nil {
		//: caller's response to absence is to fall back to defaults.
		return "", false
	}
	//: hand back the deepest Reason.
	return deepest.reason, true
}

// PublicOf walks the Unwrap chain and returns the deepest *Error.Public.
//
// Params:
//   - err: any error value.
//
// Returns:
//   - string: the Public message, or "" if no *Error is found.
func PublicOf(err error) (public string) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → empty wire-safe default.
	if deepest == nil {
		//: no *Error means nothing to surface publicly.
		return ""
	}
	//: hand back the deepest Public.
	return deepest.public
}

// PrivateOf walks the chain and returns the deepest *Error.Private.
// DIAGNOSTIC-ONLY: never expose in HTTP/gRPC responses or any user-facing
// surface. Intended for operator tooling and log correlation.
//
// Params:
//   - err: any error value.
//
// Returns:
//   - string: the Private message, or "" if no *Error is found.
func PrivateOf(err error) (private string) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → empty string.
	if deepest == nil {
		//: diagnostic accessor stays empty when there is nothing to diagnose.
		return ""
	}
	//: hand back the diagnostic string to the operator tool.
	return deepest.private
}

// FieldsOf collects the FieldValues attached to every *Error along the
// chain and returns them ordered inner-to-outer (oldest cause first,
// newest wrapper last). The returned slice is a defensive copy.
//
// Params:
//   - err: any error value.
//
// Returns:
//   - []FieldValue: the accumulated fields; empty when no *Error is found.
func FieldsOf(err error) (out []FieldValue) {
	//: pull the outermost *Error via errors.AsType; Wrap already merged fields inner-first.
	layer, ok := errors.AsType[*Error](err)
	//: absence branch — no *Error present in the chain.
	if !ok {
		//: caller expects an empty slice when no sdk layer is present.
		return nil
	}
	//: delegate to Fields() which already returns a defensive copy.
	return layer.Fields()
}

// LayerOf walks the chain and returns the deepest *Error.Layer().
//
// Params:
//   - err: any error value.
//
// Returns:
//   - int: 1..9 when an *Error with a valid layered code is found, 0 otherwise.
func LayerOf(err error) (layer int) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → unclassified (0).
	if deepest == nil {
		//: no *Error means we cannot assign a layer.
		return 0
	}
	//: delegate to the Error's own Layer method.
	return deepest.Layer()
}

// HTTPStatusOf walks the chain and returns the deepest *Error.HTTPStatus().
//
// Params:
//   - err: any error value.
//
// Returns:
//   - int: the HTTP status mapped from the deepest *Error, or 500 if none.
func HTTPStatusOf(err error) (status int) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → safe default 500.
	if deepest == nil {
		//: keep the wire behaviour predictable even when no *Error is present.
		return defaultHTTPStatus
	}
	//: delegate to the Error's own accessor.
	return deepest.HTTPStatus()
}

// ExitCodeOf walks the chain and returns the deepest *Error.ExitCode().
//
// Params:
//   - err: any error value.
//
// Returns:
//   - int: the exit code mapped from the deepest *Error, or 70 if none.
func ExitCodeOf(err error) (code int) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → default 70 (EX_SOFTWARE).
	if deepest == nil {
		//: keep the CLI behaviour predictable even when no *Error is present.
		return defaultExitCode
	}
	//: delegate to the Error's own accessor.
	return deepest.ExitCode()
}

// HasCode walks the chain and reports whether ANY *Error carries the code.
//
// Params:
//   - err: any error value.
//   - code: numeric identifier to look for.
//
// Returns:
//   - bool: true iff an *Error with a matching code is found.
func HasCode(err error, code int) (found bool) {
	//: walk every *Error layer checking the code.
	cursor := err
	//: iterate the chain until exhausted or a match is found.
	for cursor != nil {
		//: extract the nearest *Error.
		layer, ok := errors.AsType[*Error](cursor)
		//: if no further *Error exists below, stop without a match.
		if !ok {
			//: exhausted chain — no *Error left to check.
			return false
		}
		//: first match wins; early-return true.
		if layer.code == code {
			//: found it — short-circuit the walk.
			return true
		}
		//: descend into the cause to keep searching deeper.
		cursor = layer.source
	}
	//: exhausted the chain without a match.
	return false
}

// HasReason walks the chain and reports whether ANY *Error carries reason.
//
// Params:
//   - err: any error value.
//   - reason: SCREAMING_SNAKE identifier to look for.
//
// Returns:
//   - bool: true iff an *Error with a matching Reason is found.
func HasReason(err error, reason string) (found bool) {
	//: mirror the code-walk using Reason.
	cursor := err
	//: iterate the chain until exhausted or a match is found.
	for cursor != nil {
		//: extract the nearest *Error.
		layer, ok := errors.AsType[*Error](cursor)
		//: if no further *Error exists below, stop without a match.
		if !ok {
			//: exhausted chain — no *Error left to check.
			return false
		}
		//: first reason match wins.
		if layer.reason == reason {
			//: found it — short-circuit the walk.
			return true
		}
		//: descend into the cause to keep searching deeper.
		cursor = layer.source
	}
	//: exhausted the chain without a match.
	return false
}

// deepestError walks err via errors.As and returns the innermost *Error.
// Unexported because only the public accessors should use it.
//
// Params:
//   - err: any error value.
//
// Returns:
//   - *Error: the deepest *Error in the chain, or nil if none.
func deepestError(err error) (deepest *Error) {
	//: walk by unwrapping, remembering the most recent *Error we saw.
	cursor := err
	//: iterate every level of the chain.
	for cursor != nil {
		//: extract the *Error sitting at this level.
		layer, ok := errors.AsType[*Error](cursor)
		//: stop as soon as there is no longer an *Error below us.
		if !ok {
			//: break out — "deepest" is whatever we already captured.
			break
		}
		//: remember this layer; a deeper one may replace it next iteration.
		deepest = layer
		//: descend past this *Error via its explicit wrapped field.
		cursor = layer.source
	}
	//: hand back the deepest *Error we saw, or nil.
	return deepest
}
