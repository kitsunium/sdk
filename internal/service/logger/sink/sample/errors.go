// Package sample: errors.go declares the sentinels returned by the sample
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package sample

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// RateInvalid is returned when New receives a non-positive rate.
	RateInvalid = errs.Define(CodeSampleRateInvalid, "SAMPLE_RATE_INVALID",
		"Sample rate must be a positive integer",
		"service/logger/sink/sample.New called with rate <= 0")

	// DownstreamNil is returned when New receives a nil downstream sink.
	DownstreamNil = errs.Define(CodeSampleDownstreamNil, "SAMPLE_DOWNSTREAM_NIL",
		"Sample sink requires a non-nil downstream",
		"service/logger/sink/sample.New called with nil downstream")
)
