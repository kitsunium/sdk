// Package rotfile — Config value type for rotatingSink, in the parent-prefixed
// sibling of rotating_sink.go per KTN-STRUCT-ONEFILE / KTN-STRUCT-COLOCATE.
package rotfile

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

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
//
// MaxAgeDays additionally prunes rotated siblings whose modification time is
// older than the cutoff, evaluated against Clock after every rotation. It
// composes with MaxBackups (count cap) — a backup is dropped when it exceeds
// either policy. The numeric .N backup naming is unchanged; calendar pruning
// reads each sibling's on-disk mtime rather than a name-embedded stamp, so the
// retention knobs are purely additive over the existing on-disk format.
//
// RotateEvery additionally drives a TIME-triggered rotation independent of
// MaxBytes: when positive, a background ticker forces a rotation on each
// interval (the "archive every 24h" policy), composing with the size threshold
// and the MaxBackups / MaxAgeDays retention caps. It is opt-in — the zero value
// keeps the prior size/manual-only behaviour, so plain file logging never spins
// a goroutine it did not ask for.
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
	// MaxAgeDays prunes rotated siblings whose on-disk modification time is
	// older than this many days, evaluated after every rotation (manual or
	// size-triggered) against the injected Clock. A non-positive MaxAgeDays
	// disables calendar pruning, preserving the prior count-only behavior.
	MaxAgeDays int
	// Clock is the time source used for MaxAgeDays calendar pruning; the zero
	// value (nil) falls back to clock.System. Injectable so tests prune
	// deterministically without sleeping.
	Clock clock.Clock
	// RotateEvery, when strictly positive, starts a background ticker that
	// forces a rotation on each interval regardless of size — the "archive
	// every 24h" policy. A non-positive value (the zero default) disables the
	// ticker entirely: no goroutine is spawned and rotation stays size- and
	// manual-triggered. The ticker is joined on Close.
	RotateEvery time.Duration
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
}
