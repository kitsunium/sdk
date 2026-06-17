// Package rotfile — Config is the rotfile writer's value type. Since issue #93
// the canonical struct lives in core/writer (RotFileConfig) so the public facade
// (pkg/v1/logger) can re-export it like ConsoleConfig / FileConfig; this alias
// keeps the in-package name (Config) and its type identity unchanged, so Open's
// type-assertion and every existing caller compile untouched.
package rotfile

import corewriter "github.com/kitsunium/sdk/internal/core/writer"

// Config configures the "rotfile" writer. It is an alias of
// core/writer.RotFileConfig — see that type for the full field documentation
// (Path / MaxBytes / MaxBackups / Compress / MaxAgeDays / Clock / RotateEvery /
// OnError / MinLevel).
type Config = corewriter.RotFileConfig
