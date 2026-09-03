// Package logger — exposes the named, config-driven writer surface (ADR 0012):
// the WriterName / *Config aliases, the WriterSpec pair, and NewMulti, which
// resolves each named writer to a Sink and fans records out to all of them via
// Multi. Built-in console + file writers activate with a blank import of
// pkg/v1/logger/writer; s3 / cloudwatch activate with a blank import of the
// matching third-party/aws/writer package (which alone pulls the AWS SDK).
package logger

import (
	"errors"
	"slices"

	corewriter "github.com/kitsunium/sdk/internal/core/writer"
)

// StreamStderr targets os.Stderr (the zero value); StreamStdout targets stdout.
// Typed as corewriter.ConsoleStream so the constants stand alone ahead of the
// type aliases below (const → type ordering).
const (
	// StreamStderr selects os.Stderr for the console writer (zero value, so a
	// ConsoleConfig{} written without an opinion lands here — ADR 0030).
	StreamStderr corewriter.ConsoleStream = corewriter.ConsoleStderr
	// StreamStdout selects os.Stdout for the console writer. Because stdout is
	// a protocol channel for many processes, it is reachable only by name.
	StreamStdout corewriter.ConsoleStream = corewriter.ConsoleStdout
)

// WriterName is the stable alias for a registered writer key
// ("console" / "file" / "rotfile" / "s3" / "cloudwatch").
type WriterName = corewriter.Name

// ConsoleConfig configures the "console" writer (stream + optional MinLevel).
// The zero value targets os.Stderr (ADR 0030).
type ConsoleConfig = corewriter.ConsoleConfig

// FileConfig configures the "file" writer (path + optional MinLevel).
type FileConfig = corewriter.FileConfig

// S3Config configures the "s3" writer. Usable as a value without the AWS SDK;
// it resolves to a working sink only once third-party/aws/writer/s3 is imported.
type S3Config = corewriter.S3Config

// CloudWatchConfig configures the "cloudwatch" writer. Same import-gated
// resolution as S3Config.
type CloudWatchConfig = corewriter.CloudWatchConfig

// RotFileConfig configures the "rotfile" writer — a size- and/or age-capped,
// optionally gzip-compressed on-disk file that rotates Path -> Path.1 … up to
// MaxBackups (Path / MaxBytes / MaxBackups / MaxAgeDays / Compress / RotateEvery
// / MinLevel). Usable as a value once
// github.com/kitsunium/sdk/pkg/v1/logger/writer is blank-imported (it
// self-registers the "rotfile" factory alongside console and file); pass it via
// WriterSpec{Name: "rotfile", Config: cfg} to NewMulti.
type RotFileConfig = corewriter.RotFileConfig

// ConsoleStream selects which standard stream the console writer targets. Its
// zero value is StreamStderr (ADR 0030).
type ConsoleStream = corewriter.ConsoleStream

// CredentialProvider yields short-lived credentials on demand for the network
// writers; the SDK never logs or wraps the returned material.
type CredentialProvider = corewriter.CredentialProvider

// CredentialValue is the opaque, redacting credential set returned by a
// CredentialProvider; its String output is always "<redacted>".
type CredentialValue = corewriter.CredentialValue

// NewCredentialValue builds a CredentialValue from AWS SigV4 material. An empty
// sessionToken is valid for long-lived keys.
func NewCredentialValue(accessKeyID, secretAccessKey, sessionToken string) CredentialValue {
	//: delegate to the core constructor; this façade adds no behaviour.
	return corewriter.NewCredentialValue(accessKeyID, secretAccessKey, sessionToken)
}

// WriterSpec names a writer and carries its concrete config. Read at call sites
// as logger.WriterSpec{Name: "file", Config: logger.FileConfig{Path: …}}. It is
// a type alias onto internal/core/writer, so the public type is identity-equal
// to the internal writer model (alias-based public surface, zero runtime cost).
type WriterSpec = corewriter.Spec

// NewMulti builds a Logger that fans every record out to all specs, each
// resolved to a Sink by its registered factory and composed through Multi.
// Records are filtered at min (the handler-global level); a spec's own MinLevel
// can restrict an individual writer further.
//
// Each writer's package MUST be imported for its Name to resolve: blank-import
// pkg/v1/logger/writer for console + file, and third-party/aws/writer/{s3,cloudwatch}
// for the AWS writers. An unresolved Name returns the registry's
// WriterUnknownName; an empty specs list returns WriterSpecInvalid.
//
//	import (
//	    "github.com/kitsunium/sdk/pkg/v1/logger"
//	    _ "github.com/kitsunium/sdk/pkg/v1/logger/writer" // console + file
//	)
//
//	lg, err := logger.NewMulti(logger.LevelInfo,
//	    logger.WriterSpec{Name: "console", Config: logger.ConsoleConfig{Stream: logger.StreamStderr}},
//	    logger.WriterSpec{Name: "file",    Config: logger.FileConfig{Path: "/var/log/app.log"}},
//	)
func NewMulti(min Level, specs ...WriterSpec) (lg Logger, err error) {
	//: refuse a no-destination logger so the misconfiguration surfaces early.
	if len(specs) == 0 {
		//: documented sentinel — caller must supply at least one writer.
		return nil, WriterSpecInvalid
	}
	//: pre-size the branch slate with exact cardinality.
	branches := make([]Sink, 0, len(specs))
	//: rollback closes every opened sink (reverse order) on a failed build so
	//: no file descriptor / async drainer leaks, folding any close failure
	//: behind cause so none is dropped; a clean rollback returns cause verbatim.
	//: A local closure keeps this helper off the file's top-level surface.
	rollback := func(cause error) error {
		closeErrs := make([]error, 0, len(branches))
		//: walk in reverse so the most recently opened sink unwinds first.
		for _, branch := range slices.Backward(branches) {
			//: check each close — a failure joins the returned chain, never drops.
			if cErr := branch.Close(); cErr != nil {
				closeErrs = append(closeErrs, cErr)
			}
		}
		//: no close failed — the rollback is clean.
		if len(closeErrs) == 0 {
			//: keep the typed cause (errs.HasCode / errors.Is stay intact).
			return cause
		}
		//: fold close failures behind the primary cause for full observability.
		return errors.Join(append([]error{cause}, closeErrs...)...)
	}
	//: resolve each named writer to a Sink via the core registry.
	for _, spec := range specs {
		//: Open resolves the factory and validates the config (origin wins).
		sink, oErr := corewriter.Open(spec.Name, spec.Config)
		//: a failing writer aborts construction.
		if oErr != nil {
			//: roll back the sinks already opened, then surface the cause.
			return nil, rollback(oErr)
		}
		branches = append(branches, sink)
	}
	//: route through NewWithSink so the same encoder + version stamping applies.
	lg, err = NewWithSink(SinkConfig{
		Sink:     Multi(branches...),
		Encoder:  TextEncoder(),
		MinLevel: min,
	})
	//: NewWithSink failed after every sink opened.
	if err != nil {
		//: roll them all back before surfacing the construction error.
		return nil, rollback(err)
	}
	//: success — the returned Logger now owns the branches via Multi.
	return lg, nil
}
