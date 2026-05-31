// Package logger — declares the WriterEntryConfig DTO consumed by FromConfig.
// A WriterEntryConfig names a registered writer and carries its raw, codec-
// decoded option map; FromConfig hands that map to the writer's Decoder (or a
// default mapping) to obtain a typed writer.Config.
package logger

// WriterEntryConfig names one writer in a TopologyConfig and carries its raw
// option map as decoded from the config blob. Name MUST match a writer
// registered via a blank-import; Options is the per-writer option bag handed to
// the writer's Decoder (when it implements one) or passed straight to the
// factory's Open otherwise. Option values are never echoed into an error — the
// SECRET GATE redacts them. The Config role suffix marks it a config DTO
// (KTN-STRUCT-ROLE).
type WriterEntryConfig struct {
	// Name is the registered writer key ("console" / "file" / "s3" / …).
	Name string `json:"name" yaml:"name" toml:"name"`
	// Options is the raw, codec-decoded per-writer option map.
	Options map[string]any `json:"config" yaml:"config" toml:"config"`
}
