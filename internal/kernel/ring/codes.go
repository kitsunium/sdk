// Package ring — range 0.1.3.* (ADR 0005 kernel/ring block).
package ring

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.1.3.0 - 0.1.3.255

// CodeRingFull identifies a TryWrite call on a saturated ring buffer.
const CodeRingFull errs.Code = 0x00_01_03_01 // 0.1.3.1

// CodeRingEmpty identifies a TryRead call on an empty ring buffer.
const CodeRingEmpty errs.Code = 0x00_01_03_02 // 0.1.3.2

// CodeRingCapZero identifies a New call made with a non-positive capacity.
const CodeRingCapZero errs.Code = 0x00_01_03_03 // 0.1.3.3
