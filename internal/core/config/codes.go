// Package config — range 0.2.10.* (ADR 0028 core/config block).
package config

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.10.0 - 0.2.10.255

// CodeConfigSourceFailed identifies a Source.Load that failed to read its backing
// store (missing file, unreadable env, etc.).
const CodeConfigSourceFailed errs.Code = 0x00_02_0A_01 // 0.2.10.1

// CodeConfigDecodeFailed identifies a merged-map → target-struct decode failure (type
// mismatch, malformed value).
const CodeConfigDecodeFailed errs.Code = 0x00_02_0A_02 // 0.2.10.2

// CodeConfigValidationFailed identifies a decoded config whose Validate() returned an
// error (the wrap cause is the validation error).
const CodeConfigValidationFailed errs.Code = 0x00_02_0A_03 // 0.2.10.3

// CodeConfigWatchFailed identifies a Watcher that could not observe its source (stat
// failure, unsupported target).
const CodeConfigWatchFailed errs.Code = 0x00_02_0A_04 // 0.2.10.4

// CodeConfigSchemaInvalid identifies a Schema refused AT CONSTRUCTION: a
// malformed key, a key that names nothing in the target type, a duplicate key,
// a default value that cannot travel through the loader's JSON round trip, or a
// default that violates the very constraint the schema declares for its own key.
// It is never a load outcome — no source was ever read.
const CodeConfigSchemaInvalid errs.Code = 0x00_02_0A_05 // 0.2.10.5

// CodeConfigKeyMissing identifies a load in which the schema declared a key
// REQUIRED and no source supplied it. It is a load outcome, not a construction
// one: the schema was well-formed, the deployment was not. Every missing key is
// named in one error — an operator who restarts a service to discover the next
// one is the failure a report prevents.
const CodeConfigKeyMissing errs.Code = 0x00_02_0A_06 // 0.2.10.6

// CodeConfigUnknownKey identifies a load in which a source supplied a key the
// target type cannot address. The default is to REFUSE it, because the usual
// cause is a typo in an environment variable and the usual symptom of ignoring
// it is a production incident in which a setting silently never applied.
const CodeConfigUnknownKey errs.Code = 0x00_02_0A_07 // 0.2.10.7
