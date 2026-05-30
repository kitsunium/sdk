// Package rotfile — Config value type for rotatingSink, in the parent-prefixed
// sibling of rotating_sink.go per KTN-STRUCT-ONEFILE / KTN-STRUCT-COLOCATE.
package rotfile

import "github.com/kitsunium/sdk/internal/core/logger/level"

// Config configures the "rotfile" writer — a size-capped, on-disk file that
// rotates when a write would exceed MaxBytes. It is the concrete config a
// caller passes as writer.Config to writer.Open("rotfile", …); Path is required,
// an empty Path is rejected with the rotfile open sentinel.
//
// On rotation the active file is renamed Path -> Path.1 (shifting Path.1 ->
// Path.2 … up to MaxBackups, dropping the oldest; MaxBackups == 0 keeps all),
// optionally gzip'd when Compress is set, then Path is reopened fresh. Every
// reopen re-applies the O_NOFOLLOW + 0600 hardening (CWE-59), and every rotated
// .N / .N.gz sibling is forced to 0600 so a gzip never leaks default perms.
type Config struct {
	// Path is the active destination file; it is opened
	// O_APPEND|O_CREATE|O_WRONLY (+ O_NOFOLLOW on Linux) with mode 0600.
	Path string
	// MaxBytes is the size threshold: a Write that would push the active file
	// past MaxBytes triggers a rotation first. A non-positive MaxBytes disables
	// size-based rotation (the file grows unbounded).
	MaxBytes int64
	// MaxBackups caps how many rotated siblings (Path.1 … Path.N) are kept; the
	// oldest is dropped once the cap is reached. MaxBackups == 0 keeps all.
	MaxBackups int
	// Compress gzips each rotated file (Path.1 -> Path.1.gz) with mode 0600.
	Compress bool
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
}
