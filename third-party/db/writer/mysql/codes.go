// Package mysql — range 0.3.32.* (ADR 0015 service slot 0x20).
package mysql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.32.0 - 0.3.32.255

// CodeMySQLClientInitFailed identifies a failure to build the database/sql
// handle at Open time (nil credentials, an unresolvable credential provider, or
// a malformed DSN). ExitCode defaults to 74 (EX_IOERR).
const CodeMySQLClientInitFailed errs.Code = 0x00_03_20_01 // 0.3.32.1

// CodeMySQLInsertFailed identifies a failed batch INSERT on the drainer
// goroutine. ExitCode defaults to 74 (EX_IOERR).
const CodeMySQLInsertFailed errs.Code = 0x00_03_20_02 // 0.3.32.2
