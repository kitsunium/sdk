//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/config .

// Package config is the public facade for the SDK's configuration domain: layer
// configuration from env + files, [Load] into a typed struct (later sources
// override earlier), self-[Validator] the result, and [PollWatcher] for cross-OS
// hot-reload.
//
//	type Conf struct{ Port int `json:"port"` }
//	var c Conf
//	err := config.Load(&c,
//	    config.FileSource("json", "/etc/app.json"), // blank-import pkg/v1/codec
//	    config.EnvSource("APP"),                     // APP_PORT overrides the file
//	)
//
// File parsing dispatches through the codec registry — blank-import the format's
// codec (e.g. pkg/v1/codec) so it is registered. Failures surface typed
// sentinels (SourceFailed / DecodeFailed / ValidationFailed / WatchFailed).
//
// # The schema: required keys, defaults, and a closed vocabulary
//
// [NewSchema] compiles a [SchemaSpec] into a [Schema]: which keys the
// application cannot start without, the typed [Default] each key takes when
// nobody supplies it, and the constraints the decoded result must satisfy — the
// `validate` struct tags of the type, composed with any cross-field rule the
// caller adds. [LoadSchema] is [Load] with one.
//
//	type Conf struct {
//	    Port     int    `json:"port"      validate:"min=1,max=65535"`
//	    Database struct {
//	        DSN      string `json:"dsn"       validate:"required"`
//	        MaxConns int    `json:"max_conns" validate:"min=1,max=512"`
//	    } `json:"database" validate:"dive"`
//	}
//
//	schema, err := config.NewSchema[Conf](config.SchemaSpec[Conf]{
//	    Required: []string{"database.dsn"},
//	    Defaults: []config.Default{
//	        {Key: "port", Value: 8080},
//	        {Key: "database.max_conns", Value: 16},
//	    },
//	})
//	// …
//	var c Conf
//	err = config.LoadSchema(&c, schema,
//	    config.FileSource("json", "/etc/app.json"),
//	    config.EnvSource("APP"),
//	)
//
// Five properties are the reason it exists.
//
// A required key that no source supplied fails the LOAD — at start-up, before
// anything reads the value, which is the entire point of declaring one. Every
// missing key is named in ONE error rather than the first one found, so an
// operator does not restart the service once per typo. It is a key-level check,
// decided on the merged map while the key is still a key; `validate:"required"`
// is the value-level one, decided after the decode. `port = 0` satisfies the
// first and fails the second. Declare both when both are meant.
//
// A key no field of the type addresses is REFUSED by default. Ignoring it is
// the classic production incident: APP_PORTT=9090 decodes into nothing, the
// process starts on the old port, and the only evidence is the absence of an
// effect. Set [SchemaSpec].AllowUnknownKeys where the source genuinely carries
// more than this type reads — a file shared by two services, or an unprefixed
// EnvSource, which hands over every variable in the environment.
//
// A default is a LAYER, not a post-decode fallback. It is merged under every
// source before the decode, so presence is decided while the operator's key is
// still a key: an omitted "port" takes 8080, and `port = 0` written in a file
// stays 0. The layer order is default < file < env < any later source, and the
// default layer is placed first structurally — there is no way to spell a load
// in which it wins, because a default that could win is not a default. A key
// may not be both required and defaulted: the schema would fill it itself, so
// the requirement could never fire.
//
// A violation names the OPERATOR's key ("database.max_conns"), never the Go
// field, because every format is decoded through a json round trip and the json
// tag is literally the key they typed.
//
// A message never contains the value that was refused. A configuration value is
// routinely a password, a token or a connection string, and a validation
// message is the one error message designed to reach a human. The error carries
// the violation count, the first rule and the offending keys; [Schema.Check]
// returns the full report when per-key messages are wanted.
//
// A schema that contradicts itself — a default outside the bounds it also
// declares, a key naming no field of the type, the same key twice, a key both
// required and defaulted — is refused by [NewSchema] with SchemaInvalid, before
// any source is read. A schema that declares nothing is legitimate and accepts
// (and still refuses an unknown key).
//
// [SchemaSpec].Rule and [Schema.Check] speak in the vocabulary of
// pkg/v1/validation: a rule is a validation.Constraint and a report is a
// validation.Report. They are the same types, not converted ones — this domain
// composes that engine rather than reimplementing it, and a rule written for an
// HTTP body is the same value here. The schema describes the SHAPE (which keys,
// required or not, and what they default to); the constraints on a VALUE stay
// where they already are.
package config

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	svcconfig "github.com/kitsunium/sdk/internal/service/config"
)

// Source is the public alias for a configuration layer producer.
type Source = coreconfig.Source

// Validator is the public alias for a decoded config's self-check.
type Validator = coreconfig.Validator

// Watcher is the public alias for a change observer.
type Watcher = coreconfig.Watcher

// Default is the public alias for one declared key and the typed value it
// takes when NO source supplied it. A defaulted key is never also required.
type Default = coreconfig.DeclaredValue

// Schema is the public alias for a compiled configuration shape: the default
// layer it contributes, the keys it requires, the vocabulary it accepts, and
// the constraints it enforces. Build one with [NewSchema].
type Schema[T any] = svcconfig.SchemaValue[T]

// SchemaSpec is the public alias for a schema declaration. Its zero value is a
// legitimate schema: nothing required, nothing defaulted, no extra rule — but
// the `validate` tags of T still apply and an unknown key is still refused.
type SchemaSpec[T any] = svcconfig.SchemaSpec[T]

var (
	// SourceFailed is returned when a source cannot be read.
	SourceFailed = coreconfig.ConfigSourceFailed
	// DecodeFailed is returned when the merged config cannot decode into the target.
	DecodeFailed = coreconfig.ConfigDecodeFailed
	// ValidationFailed is returned when the decoded config fails Validate().
	ValidationFailed = coreconfig.ConfigValidationFailed
	// WatchFailed is returned when the watcher cannot observe its source.
	WatchFailed = coreconfig.ConfigWatchFailed
	// SchemaInvalid is returned by NewSchema when the schema contradicts the
	// type it describes, or itself. It is never a load outcome.
	SchemaInvalid = coreconfig.ConfigSchemaInvalid
	// KeyMissing is returned by LoadSchema when the schema requires keys no
	// source supplied. Its fields name every one of them.
	KeyMissing = coreconfig.ConfigKeyMissing
	// UnknownKey is returned by LoadSchema when a source supplied keys the
	// target type cannot address and the schema did not opt out.
	UnknownKey = coreconfig.ConfigUnknownKey
)

// Load merges sources into target (later overrides earlier), decodes, and
// validates (when target implements Validator).
//
// It declares no schema: nothing is required, nothing is defaulted, and a key
// the target cannot address is dropped exactly as encoding/json drops it. Use
// [LoadSchema] to have those caught.
func Load[T any](target *T, sources ...Source) error {
	//: delegate to the service loader.
	return svcconfig.Load(target, sources...)
}

// NewSchema compiles spec into a [Schema], refusing at construction every
// declaration that could not work: a malformed key, a key naming no field of T,
// a duplicate, a key declared both required and with a default, a default the
// decode cannot carry, and — the one that matters — a default that violates the
// constraint the schema itself declares for that key. A tag the validation
// engine refuses surfaces THAT engine's error, whose fields already name the
// field, the rule and the clause.
func NewSchema[T any](spec SchemaSpec[T]) (schema *Schema[T], err error) {
	//: delegate to the service schema compiler.
	return svcconfig.NewSchemaValue[T](spec)
}

// LoadSchema is [Load] with a compiled [Schema]: the schema's typed defaults are
// merged UNDER every source, its required keys and its vocabulary are checked
// against the merged map before anything is decoded, and its constraints run
// over the decoded result before the target's own Validate — which is still
// called, never replaced.
//
// The layer order is default < file < env < any later source. A missing
// required key and an unaddressable key are reported together, each naming all
// of its keys, so one restart tells the whole truth. A nil schema is refused by
// name rather than silently loading nothing.
func LoadSchema[T any](target *T, schema *Schema[T], sources ...Source) error {
	//: delegate to the service loader.
	return svcconfig.LoadSchema(target, schema, sources...)
}

// EnvSource returns a Source reading "PREFIX_KEY" env vars (empty prefix = all).
//
// The key a field must match is the variable name with the prefix removed and
// lower-cased, underscores kept: under EnvSource("APP"), the variable
// APP_SDM_SERVER_NAME feeds a field tagged `json:"sdm_server_name"`. The
// separating underscore belongs to the Source, and a trailing one on prefix is
// absorbed, so EnvSource("APP") and EnvSource("APP_") name the same namespace.
//
// A value is coerced to a typed Go value only when the WHOLE value is one
// complete JSON document — "8080" becomes an int64, "true" a bool. Anything
// else keeps its exact string, so identifiers that merely start like numbers
// ("0A0A01", "1500ms", "10.45.0.0/16", "2026-09-03") arrive intact.
//
// An EMPTY prefix reads the whole process environment, so pairing it with a
// schema that refuses unknown keys refuses PATH, HOME and everything else the
// shell exported. That is a fact about the source, not about the schema: give
// the source a prefix, or set [SchemaSpec].AllowUnknownKeys.
func EnvSource(prefix string) Source {
	//: delegate to the service env source.
	return svcconfig.EnvSource(prefix)
}

// FileSource returns a Source reading path and parsing it as format (the codec
// must be registered — blank-import its package, e.g. pkg/v1/codec).
func FileSource(format, path string) Source {
	//: delegate, converting the format name to the codec key type.
	return svcconfig.FileSource(codec.Format(format), path)
}

// PollWatcher returns a cross-OS Watcher that polls path every interval.
func PollWatcher(path string, interval time.Duration) Watcher {
	//: delegate to the service poll watcher.
	return svcconfig.PollWatcher(path, interval)
}
