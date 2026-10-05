// Package file registers the "file" writer factory. Importing the package
// (typically a blank import via pkg/v1/observe/logger/writer) self-registers the factory
// so writer.Open("file", logger.FileConfig{…}) resolves. The factory delegates
// to the existing append-only, symlink-hardened sink in
// service/observe/logger/sink/file and applies the optional per-writer MinLevel via
// levelgate.
//
// The factory also satisfies core/observe/logger/writer.Decoder so the default-active file
// writer is YAML/JSON/TOML-reachable through FromConfig. Recognised option keys:
//
//	path       destination file (required, non-empty string)
//	min_level  "debug" | "info" | "warn" | "error" (default info)
//
// A malformed shape (missing/empty/non-string path, wrong-type or unknown
// level) returns the shared core/observe/logger/writer.WriterConfigInvalid sentinel. Per the
// Decoder secret-gate contract the error names only the writer and the failure
// kind — never a decoded path or option value.
package file
