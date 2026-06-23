// Package config — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package config

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78) — a configuration fault is neither
// a generic internal error nor transient unavailability.
const exitConfig int = 78

var (
	// ConfigSourceFailed wraps a Source.Load read failure.
	ConfigSourceFailed = errs.Define(CodeConfigSourceFailed, "CONFIG_SOURCE_FAILED",
		"A configuration source could not be read",
		"service/config: a Source.Load failed to read its backing store",
		errs.WithExitCode(exitConfig))

	// ConfigDecodeFailed wraps a merged-map → struct decode failure.
	ConfigDecodeFailed = errs.Define(CodeConfigDecodeFailed, "CONFIG_DECODE_FAILED",
		"The configuration could not be decoded into the target",
		"service/config: merged map did not decode into the target struct",
		errs.WithExitCode(exitConfig))

	// ConfigValidationFailed wraps a decoded config's Validate() error.
	ConfigValidationFailed = errs.Define(CodeConfigValidationFailed, "CONFIG_VALIDATION_FAILED",
		"The configuration failed validation",
		"service/config: the decoded config's Validate() returned an error",
		errs.WithExitCode(exitConfig))

	// ConfigWatchFailed wraps a Watcher observation failure.
	ConfigWatchFailed = errs.Define(CodeConfigWatchFailed, "CONFIG_WATCH_FAILED",
		"The configuration watcher could not observe its source",
		"service/config: the poll watcher failed to stat its target",
		errs.WithExitCode(exitConfig))
)
