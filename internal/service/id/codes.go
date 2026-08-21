// Package id — range 0.3.39.* (ADR 0024 service/id block).
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.39.0 - 0.3.39.255

// CodeIDEntropyFailed identifies a crypto/rand.Read failure while drawing the
// random bytes of a UUID/ULID (a CSPRNG fault — extremely rare).
const CodeIDEntropyFailed errs.Code = 0x00_03_27_01 // 0.3.39.1

// CodeIDClockBackwards identifies a snowflake generation where the monotonic
// clock moved backwards past the last-issued timestamp beyond the recoverable
// same-millisecond sequence space.
const CodeIDClockBackwards errs.Code = 0x00_03_27_02 // 0.3.39.2

// CodeIDClockStalled identifies a snowflake same-millisecond overflow wait that
// gave up because the clock stopped advancing. Distinct from ClockBackwards: the
// clock did not regress, it simply never ticked, so the wait could not complete.
const CodeIDClockStalled errs.Code = 0x00_03_27_03 // 0.3.39.3
