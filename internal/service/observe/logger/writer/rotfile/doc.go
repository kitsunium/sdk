// Package rotfile — backup-name arithmetic and the per-slot move/gzip helpers
// split out of rotate.go so each function stays under KTN-FUNC-MAXLOC.
//
// Package rotfile — the config Decoder that makes "rotfile" reachable from a
// config file via pkg/v1/observe/logger.FromConfig. Kept in its own file (NOT inlined
// into rotfile.go) so the factory's Open and Decode halves never collide in one
// source. Every helper redacts: a malformed value yields RotFileDecodeFailed
// tagged with the writer name only, never the offending value (secret gate).
//
// Package rotfile — calendar pruning of rotated siblings (MaxAgeDays), split
// out of rotate.go so each function stays under KTN-FUNC-MAXLOC. The exported
// on-demand Rotate method lives beside its receiver in rotating_sink.go.
//
// Package rotfile — rotation cycle: threshold check, backup shift, optional
// gzip, and the hardened reopen. Kept out of sink.go so each function stays
// under KTN-FUNC-MAXLOC.
//
// Package rotfile — the interval-rotation tick body driven by the worker.Every
// daemon wired in newRotatingSink when Config.RotateEvery is positive. Split
// from rotating_sink.go so the daemon's tick logic and its tests sit in their
// own source/test pair.
//
// Package rotfile — the exported on-demand Rotate method (SIGHUP / logrotate
// integration), split out of rotating_sink.go so the receiver file stays under
// KTN-FUNC-MAXLOC. The calendar-pruning helpers it delegates to live in
// prune.go.
//
// Package rotfile — rotatingSink (the rotating Sink implementation) plus the
// open/reopen hardening shared with the rotation cycle. Its Config lives in the
// parent-prefixed sibling rotating_sink_config.go (KTN-STRUCT-ONEFILE).
//
// Package rotfile — Config is the rotfile writer's value type. Since issue #93
// the canonical struct lives in core/observe/logger/writer (RotFileConfig) so the public facade
// (pkg/v1/observe/logger) can re-export it like ConsoleConfig / FileConfig; this alias
// keeps the in-package name (Config) and its type identity unchanged, so Open's
// type-assertion and every existing caller compile untouched.
//
// Package rotfile registers the "rotfile" writer factory (ADR 0014): a
// size-capped, on-disk file sink that rotates when a write would exceed
// MaxBytes and optionally gzips each rotated file. Importing the package
// (typically a blank import via pkg/v1/observe/logger/writer) self-registers the
// factory so writer.Open("rotfile", Config{…}) resolves.
//
// Unlike service/observe/logger/writer/file, this sink owns its descriptor directly because it
// must close + rename + reopen Path across a rotation; it cannot delegate to
// the append-only sink/file. The security-critical hardening of sink/file is
// preserved and, crucially, RE-RUN on every reopen: the symlink refusal +
// O_NOFOLLOW + 0600 checks fire each time Path is recreated after a rename/gzip
// cycle, not only at first New (CWE-59). Rotated .N and .N.gz siblings are
// forced to 0600 so a gzip never leaks default permissions.
//
// Durability note: the rename is not followed by a directory fsync, so a crash
// between rename and reopen can leave Path missing until the next write — an
// accepted crash window for logs, documented rather than paid for on every
// rotation.
//
// Package rotfile — the wrap points every failure of this writer goes through,
// so each carries its code from internal/core/observe/logger/writer/rotfile.
package rotfile
