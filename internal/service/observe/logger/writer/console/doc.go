// Package console registers the "console" writer factory. Importing the package
// (typically a blank import via pkg/v1/observe/logger/writer) self-registers the factory
// so writer.Open("console", logger.ConsoleConfig{…}) resolves. The factory
// delegates to the existing terminal sink in service/observe/logger/sink/console and
// applies the optional per-writer MinLevel via levelgate.
//
// The factory also satisfies core/observe/logger/writer.Decoder so the default-active console
// writer is YAML/JSON/TOML-reachable through FromConfig. Recognised option keys:
//
//	target     "stderr" (default) | "stdout"
//	min_level  "debug" | "info" | "warn" | "error" (default info)
//
// A malformed shape (wrong scalar type, unknown target, unknown level) returns
// the shared core/observe/logger/writer.WriterConfigInvalid sentinel. Per the Decoder
// secret-gate contract the error names only the writer and the failure kind —
// never a decoded option value.
package console
