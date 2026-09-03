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

var (
	// SourceFailed is returned when a source cannot be read.
	SourceFailed = coreconfig.ConfigSourceFailed
	// DecodeFailed is returned when the merged config cannot decode into the target.
	DecodeFailed = coreconfig.ConfigDecodeFailed
	// ValidationFailed is returned when the decoded config fails Validate().
	ValidationFailed = coreconfig.ConfigValidationFailed
	// WatchFailed is returned when the watcher cannot observe its source.
	WatchFailed = coreconfig.ConfigWatchFailed
)

// Load merges sources into target (later overrides earlier), decodes, and
// validates (when target implements Validator).
func Load[T any](target *T, sources ...Source) error {
	//: delegate to the service loader.
	return svcconfig.Load(target, sources...)
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
