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

	// ConfigSchemaInvalid is returned by the Schema CONSTRUCTOR, never by a
	// load: the schema itself contradicts the type it declares, or contradicts
	// itself. Its fields name the key and the clause that failed and NEVER the
	// value, because a default may be a credential the author wrote in source.
	//
	// It is the ADR 0031 half that must not be confused with the other: a
	// configuration with NO schema is legitimate and loads, while a schema that
	// declares a default outside the bounds it also declares would silently
	// produce an invalid configuration whenever the operator omitted the key —
	// a failure that appears only on the deployment where nobody set it.
	ConfigSchemaInvalid = errs.Define(CodeConfigSchemaInvalid, "CONFIG_SCHEMA_INVALID",
		"The configuration schema cannot be built as declared",
		"service/config: a Schema was refused at construction; the fields name the key and the clause that failed",
		errs.WithExitCode(exitConfig))

	// ConfigKeyMissing is returned by a LOAD when the schema declares a key
	// required and no source supplied it. Its fields name every missing key —
	// all of them, in one error — and the count. It never names a value,
	// because a missing key has none and a neighbouring one may be a secret.
	//
	// It is distinct from the validation domain's `required` rule on purpose,
	// and the difference is the one this domain exists to keep: this sentinel
	// answers "was the KEY supplied?", decided on the merged map while the key
	// is still a key; validation's `required` answers "is the VALUE non-zero?",
	// decided after the decode, where an absent key and an explicit zero are
	// the same bytes. `port = 0` satisfies this one and fails that one.
	ConfigKeyMissing = errs.Define(CodeConfigKeyMissing, "CONFIG_KEY_MISSING",
		"The configuration is missing required keys",
		"service/config: a schema declared keys required and no source supplied them",
		errs.WithExitCode(exitConfig))

	// ConfigUnknownKey is returned by a LOAD when a source supplied a key the
	// target type cannot address, and the schema did not opt out of the check.
	//
	// Refusing is the default because the alternative has a name: a typo in an
	// environment variable that passes silently. `APP_PORTT=9090` decodes into
	// nothing, the process starts on the old port, and the only evidence is the
	// absence of an effect. A schema is the statement "these are the keys I
	// read", so a key outside it is either a mistake or a deliberate one the
	// author opts into by name.
	ConfigUnknownKey = errs.Define(CodeConfigUnknownKey, "CONFIG_UNKNOWN_KEY",
		"The configuration carries keys the target does not define",
		"service/config: a source supplied keys no field of the target addresses",
		errs.WithExitCode(exitConfig))
)
