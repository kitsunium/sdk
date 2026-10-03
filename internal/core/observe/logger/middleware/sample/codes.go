// Package sample declares the codes and sentinels of the logger's sampling
// middleware, internal/service/observe/logger/middleware/sample: range
// 0.3.20.*, allocated to that engine (ADR 0005
// service/observe/logger/middleware/sample block) and declared here since ADR
// 0160, so the engine declares none.
package sample

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.20.0 - 0.3.20.255

// CodeSampleRateInvalid identifies a New call with a non-positive rate.
const CodeSampleRateInvalid errs.Code = 0x00_03_14_01 // 0.3.20.1

// CodeSampleDownstreamNil identifies a New call made with a nil downstream.
const CodeSampleDownstreamNil errs.Code = 0x00_03_14_02 // 0.3.20.2
