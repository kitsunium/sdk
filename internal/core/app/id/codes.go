// Package id — ranges 0.2.7.* (ADR 0024 core/app/id block) and 0.3.39.*
// (ADR 0024 service/app/id block, declared here since ADR 0160).
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.7.0 - 0.2.7.255

// CodeUnknownScheme identifies a New/Lookup call naming a Scheme that no
// imported package has registered (a missing blank-import of the scheme).
const CodeUnknownScheme errs.Code = 0x00_02_07_01 // 0.2.7.1

// CodeDuplicateRegistration identifies a boot-time collision on the Generator
// registry: a nil scheme, or a distinct generator claiming an already-registered
// Scheme. Surfaced via panic at boot (see registry.go). Reason
// DUPLICATE_REGISTRATION keeps the bracket header a valid (code,reason) pairing
// per ADR 0005, mirroring codec/transform (0.2.2.1 / 0.2.5.5).
const CodeDuplicateRegistration errs.Code = 0x00_02_07_05 // 0.2.7.5

// range: 0.3.39.0 - 0.3.39.255
//
// The generators' outcomes. The range was allocated to internal/service/app/id,
// whose schemes raise these codes, and it is declared here with the registry's
// so that every code of the domain is in one place (ADR 0160). A code keeps
// the value its allocation gave it whichever layer declares it, so the layer
// byte still reads 3.

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
