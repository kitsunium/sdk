// Package sample: codes.go — range 0.3.20.* (ADR 0005 service/logger/middleware/sample block).
package sample

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.20.0 - 0.3.20.255

// CodeSampleRateInvalid identifies a New call with a non-positive rate.
const CodeSampleRateInvalid errs.Code = 0x00_03_14_01 // 0.3.20.1

// CodeSampleDownstreamNil identifies a New call made with a nil downstream.
const CodeSampleDownstreamNil errs.Code = 0x00_03_14_02 // 0.3.20.2
