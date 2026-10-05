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
