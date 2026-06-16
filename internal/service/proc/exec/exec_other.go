//go:build !unix && !windows

// Package exec — non-Unix degrade: process supervision via fork/exec with
// credentials and process groups is a Unix facility, so Start returns the typed
// UnsupportedPlatform sentinel and the package still compiles on every GOOS.
package exec

import (
	"context"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Start reports UnsupportedPlatform on non-Unix targets: the credential,
// process-group, and wait4 primitives this package builds on have no portable
// equivalent off Unix. The signature matches the Unix build so callers compile
// identically everywhere.
func Start(ctx context.Context, spec coreproc.Spec) (proc coreproc.Process, err error) {
	//: nothing to spawn here; degrade to the typed sentinel, never a panic.
	return nil, coreproc.UnsupportedPlatform
}
