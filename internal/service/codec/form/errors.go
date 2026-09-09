// Package form — declares the sentinel *errs.Error values for the
// application/x-www-form-urlencoded codec.
//
// There is deliberately no MarshalFailed sentinel: once asValues has accepted
// the argument shape, percent-escaping a map of strings is a total function —
// no byte sequence can make it fail. Defining an unreachable sentinel would
// put a code in the registry that no test can ever provoke.
package form

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// ValueInvalid fires when the caller does not pass a urlencoded-shaped
	// value (url.Values / map[string][]string / map[string]string, or a
	// non-nil pointer to one).
	ValueInvalid = errs.Define(CodeFormValueInvalid, "VALUE_INVALID",
		"form codec requires a url.Values-shaped value",
		"service/codec/form: Marshal/Append/Unmarshal called with an unsupported argument shape")

	// UnmarshalFailed wraps a refusal from net/url.ParseQuery or a decode
	// bound (maxFormBytes / maxFormPairs).
	UnmarshalFailed = errs.Define(CodeFormUnmarshalFailed, "UNMARSHAL_FAILED",
		"form decoding failed",
		"service/codec/form: net/url.ParseQuery returned an error or a decode bound was exceeded")

	// MultiValue fires when a repeated key is decoded into a map[string]string
	// target, which can hold exactly one value per key.
	MultiValue = errs.Define(CodeFormMultiValue, "MULTI_VALUE",
		"form key carries multiple values",
		"service/codec/form: map[string]string target cannot hold a repeated key")
)
