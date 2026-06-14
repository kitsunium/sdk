// Package exec — the keystone spawn primitive: turns a coreproc.Spec into a
// running, supervised process via fork/exec with credentials, a private process
// group/session, and best-effort scheduling attributes.
//
// The platform-portable surface is Start; the concrete fork/exec lives in
// exec_unix.go (every Unix GOOS) with a degrading no-op in exec_other.go so the
// package compiles on every target. Errors are the central coreproc sentinels,
// wrapped (never re-Defined) with errs.Wrap.
package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// validateSpec rejects a malformed spec before any OS work happens. An empty
// Path is the one structural invariant the port mandates; everything else is
// resolved (and may fail with its own typed error) during the spawn.
func validateSpec(spec coreproc.Spec) error {
	//: an empty executable path can never fork/exec — fail fast with INVALID_SPEC.
	if spec.Path == "" {
		//: bare sentinel: there is no cause to wrap, only a contract violation.
		return coreproc.InvalidSpec
	}
	//: the spec carries a runnable path; defer all other resolution to the spawn.
	return nil
}
