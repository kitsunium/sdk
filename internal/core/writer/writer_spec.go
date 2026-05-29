// Package writer — Spec value type, in its own file per the
// one-exported-struct-per-file convention.
package writer

// Spec names a writer and carries its concrete config. It is the unit
// pkg/v1/logger.NewMulti fans out over; read at call sites as
// Spec{Name: "file", Config: FileConfig{Path: …}}. pkg/v1/logger re-exports it
// as logger.WriterSpec (a type alias, zero runtime cost) so the public surface
// stays identity-equal to this internal model. Named Spec (not WriterSpec) to
// avoid the writer.WriterSpec package stutter.
type Spec struct {
	// Name is the registered writer key resolved against the registry.
	Name Name
	// Config is the writer's concrete config value (ConsoleConfig, FileConfig,
	// S3Config, CloudWatchConfig); the resolved factory type-asserts it.
	Config Config
}
