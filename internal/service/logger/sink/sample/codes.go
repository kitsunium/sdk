// Package sample: codes.go — range 5100-5199 reserved for the sample Sink.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package sample

// range: 5100-5199

// CodeSampleRateInvalid identifies a New call with a non-positive rate.
const CodeSampleRateInvalid int = 5101

// CodeSampleDownstreamNil identifies a New call made with a nil downstream.
const CodeSampleDownstreamNil int = 5102
