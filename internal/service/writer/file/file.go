// Package file registers the "file" writer factory. Importing the package
// (typically a blank import via pkg/v1/logger/writer) self-registers the factory
// so writer.Open("file", logger.FileConfig{…}) resolves. The factory delegates
// to the existing append-only, symlink-hardened sink in
// service/logger/sink/file and applies the optional per-writer MinLevel via
// levelgate.
//
// The factory also satisfies core/writer.Decoder so the default-active file
// writer is YAML/JSON/TOML-reachable through FromConfig. Recognised option keys:
//
//	path       destination file (required, non-empty string)
//	min_level  "debug" | "info" | "warn" | "error" (default info)
//
// A malformed shape (missing/empty/non-string path, wrong-type or unknown
// level) returns the shared core/writer.WriterConfigInvalid sentinel. Per the
// Decoder secret-gate contract the error names only the writer and the failure
// kind — never a decoded path or option value.
package file

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered file factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&fileFactory{})

// fileFactory builds a file Sink from a writer.FileConfig.
type fileFactory struct{}

// Name reports the canonical key "file".
func (*fileFactory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "file"
}

// Open opens the configured path through the hardened file sink and wraps it in
// the per-writer level gate. A Config of the wrong concrete type yields the
// shared WriterConfigInvalid sentinel; an empty Path surfaces the file sink's
// own PathEmpty sentinel (origin wins).
func (*fileFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.FileConfig)
	//: type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: delegate to the hardened append-only sink (validates path + symlink).
	base, oerr := filesink.New(c.Path)
	//: forward the sink's typed error unchanged (origin wins).
	if oerr != nil {
		//: PathEmpty / OpenFailed already carry the right code/reason.
		return nil, oerr
	}
	//: apply the optional per-writer floor over the opened file sink.
	return levelgate.New(base, c.MinLevel), nil
}

// Decode translates a raw topology option map into a FileConfig. It is the
// YAML/JSON/TOML entry point used by FromConfig: it requires a non-empty
// "path" string and recognises an optional "min_level". A missing/empty/
// wrong-type path, or a wrong-type/unknown level, returns the shared
// WriterConfigInvalid sentinel — the offending value is never echoed
// (secret-gate contract), only the writer name is attached.
func (*fileFactory) Decode(raw map[string]any) (cfg writer.Config, err error) {
	//: accumulate into a zero config; the mandatory path is filled below.
	c := writer.FileConfig{}
	//: the destination path is mandatory and must be a non-empty string.
	if perr := decodePath(raw, &c.Path); perr != nil {
		//: redacted: names only the writer, never the decoded path.
		return nil, perr
	}
	//: map the optional severity floor; a malformed value aborts redacted.
	if lerr := decodeMinLevel(raw, &c.MinLevel); lerr != nil {
		//: redacted: names only the writer, never the decoded level value.
		return nil, lerr
	}
	//: hand back the typed config Open type-asserts.
	return c, nil
}

// decodePath maps the mandatory "path" option onto dst. The key is required: an
// absent key, a non-string value, or an empty string all yield the redacted
// WriterConfigInvalid sentinel rather than deferring an empty path to the sink.
// The value is never echoed (secret gate).
func decodePath(raw map[string]any, dst *string) error {
	//: path is mandatory; absence is a malformed shape, not a default.
	v, present := raw["path"]
	//: a missing path is rejected up front (redacted).
	if !present {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: the path must be a string scalar; reject any other shape.
	s, ok := v.(string)
	//: a non-string or empty path is a malformed shape (redacted).
	if !ok || s == "" {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: assign the validated non-empty path.
	*dst = s
	//: path mapped cleanly.
	return nil
}

// decodeMinLevel maps the optional "min_level" option onto dst. An absent key
// inherits the handler-global floor (zero Level). A non-string value or an
// unparsable name yields the redacted WriterConfigInvalid sentinel; ParseLevel
// is redaction-safe and never echoes its input.
func decodeMinLevel(raw map[string]any, dst *level.Level) error {
	//: absent min_level inherits the handler-global level (zero value).
	v, present := raw["min_level"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its inherit default.
		return nil
	}
	//: the level must be a canonical name (string); reject other shapes.
	s, ok := v.(string)
	//: a non-string level is a malformed shape (redacted).
	if !ok {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: ParseLevel is redaction-safe — it returns a sentinel, never the input.
	lvl, perr := level.ParseLevel(s)
	//: an unknown level name is a malformed shape (redacted).
	if perr != nil {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: apply the parsed floor.
	*dst = lvl
	//: level mapped cleanly.
	return nil
}

// configInvalid returns the shared WriterConfigInvalid sentinel tagged with the
// writer name only. It never carries a decoded option value, honouring the
// Decoder secret-gate contract; origin-wins keeps errors.Is matching the
// sentinel.
func configInvalid() error {
	//: attach the writer name (never an option value) for offender ID.
	return errs.Wrap(writer.WriterConfigInvalid, errs.WrapParams{}, errs.String("writer", "file"))
}
