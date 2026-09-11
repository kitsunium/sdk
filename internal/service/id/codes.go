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

// CodeIDInvalidSize identifies a NanoID constructor call naming a non-positive
// identifier length. A zero-length identifier identifies nothing, so the
// constructor refuses rather than hand back a generator that mints "" forever
// (ADR 0031).
const CodeIDInvalidSize errs.Code = 0x00_03_27_04 // 0.3.39.4

// CodeIDInvalidPrefix identifies a TypeID constructor or parse call whose type
// prefix is empty or breaks the lowercase-ASCII shape. The prefix IS the value
// of the scheme — an identifier that cannot name its own type is the degraded
// identifier ADR 0031 forbids minting silently.
const CodeIDInvalidPrefix errs.Code = 0x00_03_27_05 // 0.3.39.5

// CodeIDMalformed identifies a parse that rejected its input: a wrong length, a
// symbol outside the scheme's alphabet, or a magnitude past what the scheme's
// fixed width can represent. Carries a "rule" field naming which of the three
// fired; never echoes the input.
const CodeIDMalformed errs.Code = 0x00_03_27_06 // 0.3.39.6

// CodeIDTimestampRange identifies a generation where the clock sits outside the
// window the scheme's timestamp field can represent — the KSUID 32-bit second
// counter, whose epoch starts in 2014 and saturates in 2150. Encoding it anyway
// would wrap the prefix and destroy the sortability the format exists for.
const CodeIDTimestampRange errs.Code = 0x00_03_27_07 // 0.3.39.7
