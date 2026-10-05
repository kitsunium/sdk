package rotfile

import corewriter "github.com/kitsunium/sdk/internal/core/observe/logger/writer"

// Config configures the "rotfile" writer. It is an alias of
// core/observe/logger/writer.RotFileConfig — see that type for the full field documentation
// (Path / MaxBytes / MaxBackups / Compress / MaxAgeDays / Clock / RotateEvery /
// OnError / MinLevel).
type Config = corewriter.RotFileConfig
