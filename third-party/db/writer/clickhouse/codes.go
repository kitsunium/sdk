// Package clickhouse — range 0.3.33.* (ADR 0015 service slot 0x21).
package clickhouse

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.33.0 - 0.3.33.255

// CodeCHClientInitFailed identifies a failure to build the database/sql handle
// at Open time (nil/invalid table or an unresolvable credential provider).
// ExitCode defaults to 74 (EX_IOERR).
const CodeCHClientInitFailed errs.Code = 0x00_03_21_01 // 0.3.33.1

// CodeCHInsertFailed identifies a failed batch INSERT on the drainer goroutine.
// ExitCode defaults to 74 (EX_IOERR).
const CodeCHInsertFailed errs.Code = 0x00_03_21_02 // 0.3.33.2
