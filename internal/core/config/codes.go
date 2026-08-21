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
