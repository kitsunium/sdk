// Package logger — declares the TopologyConfig DTO consumed by FromConfig.
// A TopologyConfig is the decoded shape of a logger config file: a global level
// plus an ordered list of named writer entries. It is a plain data carrier with
// no behaviour — the construction logic lives in FromConfig.
package logger

// TopologyConfig is the decoded logger configuration: a global Level (parsed by
// the same names the level package prints — "debug" / "info" / "warn" /
// "error", case-insensitive, empty defaults to info) and the ordered Writers
// fanned out to. FromConfig unmarshals a config blob into a TopologyConfig via a
// consumer-registered codec, then resolves each entry against the writer
// registry. The Config role suffix marks it a config DTO (KTN-STRUCT-ROLE).
type TopologyConfig struct {
	// Level is the minimum severity emitted, by name; empty defaults to info.
	Level string `json:"level" yaml:"level" toml:"level"`
	// Writers is the ordered set of named writer entries to fan records out to.
	Writers []WriterEntryConfig `json:"writers" yaml:"writers" toml:"writers"`
}
