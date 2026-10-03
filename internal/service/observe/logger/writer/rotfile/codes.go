// Package rotfile — range 0.3.27.* (ADR 0014 service slot 0x1b).
package rotfile

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.27.0 - 0.3.27.255

// CodeRotFileOpenFailed identifies a (re)open whose os.OpenFile invocation
// failed or whose target resolved to a symlink (CWE-59). It fires both at
// construction and on every reopen after a rotation, so the symlink/0600
// hardening re-runs for the freshly created Path.
const CodeRotFileOpenFailed errs.Code = 0x00_03_1B_01 // 0.3.27.1

// CodeRotFileRotateFailed identifies a rotation step that failed — the
// rename shift across the .N backups, the optional gzip compaction, or the
// 0600 chmod of a rotated sibling.
const CodeRotFileRotateFailed errs.Code = 0x00_03_1B_02 // 0.3.27.2

// CodeRotFileWriteFailed identifies a Write whose underlying *os.File
// returned an error; ExitCode defaults to 74 (EX_IOERR).
const CodeRotFileWriteFailed errs.Code = 0x00_03_1B_03 // 0.3.27.3

// CodeRotFileDecodeFailed identifies a config-map value of an unexpected shape
// while decoding a "rotfile" writer entry from a config file (the Decoder
// path). It is a configuration error (EX_CONFIG 78), distinct from the I/O
// sentinels, and never echoes the offending value (secret gate).
const CodeRotFileDecodeFailed errs.Code = 0x00_03_1B_04 // 0.3.27.4
