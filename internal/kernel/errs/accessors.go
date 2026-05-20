// Package errs — provides package-level introspection helpers
// that walk a cause chain and read fields from the deepest *Error
// encountered. Consumers of pkg/v1/errs use re-exports of these.
package errs

import "errors"

// CodeOf walks the chain and returns the deepest *Error's typed Code.
func CodeOf(err error) (c Code, ok bool) {
	//: reuse the shared traversal helper.
	deepest := deepestError(err)
	//: absence path → zero pair by contract.
	if deepest == nil {
		//: no *Error in the chain; surface the documented zero pair.
		return 0, false
	}
	//: hand back the deepest Code value.
	return deepest.code, true
}

// ReasonOf walks the Unwrap chain and returns the deepest *Error.Reason.
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

// PublicOf walks the Unwrap chain and returns the deepest *Error.Public,
// or "" when no *Error is present in the chain.
func PublicOf(err error) string {
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

// PrivateOf walks the chain and returns the deepest *Error.Private,
// or "" when no *Error is present in the chain.
// DIAGNOSTIC-ONLY: never expose in HTTP/gRPC responses or any user-facing
// surface. Intended for operator tooling and log correlation.
func PrivateOf(err error) string {
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
func FieldsOf(err error) []FieldValue {
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

// HTTPStatusOf walks the chain and returns the deepest *Error.HTTPStatus().
func HTTPStatusOf(err error) int {
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
func ExitCodeOf(err error) int {
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

// HasReason walks the chain and reports whether ANY *Error carries reason.
func HasReason(err error, reason string) bool {
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
func deepestError(err error) *Error {
	var deepest *Error
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
