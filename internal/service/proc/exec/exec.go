package exec

import (
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// reservedEnvPrefix begins the name of every variable of the SDK's own spawn
// protocol — the trampoline's payload, its cgroup placement, its handshake
// descriptor (reservedEnvKeys lists them). A child reads the FIRST of two equal
// keys and the caller's Spec.Env comes first, so a caller naming one would
// override what the spawn tells its child: Start refuses it (#726).
const reservedEnvPrefix string = "__KITSUNIUM_SDK_PROC_"

// validateSpec rejects a malformed spec before any OS work happens: an empty
// Path, the one structural invariant the port mandates, and an environment that
// names a variable of the spawn protocol. Everything else is resolved (and may
// fail with its own typed error) during the spawn.
func validateSpec(spec coreproc.Spec) error {
	//: an empty executable path can never fork/exec — fail fast with INVALID_SPEC.
	if spec.Path == "" {
		//: bare sentinel: there is no cause to wrap, only a contract violation.
		return coreproc.InvalidSpec
	}
	//: a key of the spawn protocol would hijack it — refused before any fork;
	//: the rest of the spec is resolved (and may fail) during the spawn.
	return checkReservedEnv(spec.Env)
}

// checkReservedEnv refuses an environment naming a variable of the spawn
// protocol with RESERVED_ENV, naming the key. It is its own function so
// validateSpec stays inlinable.
func checkReservedEnv(env []string) error {
	//: a key of the protocol, in any case.
	if key, found := reservedEnvKey(env); found {
		//: RESERVED_ENV naming the key.
		return wrapReservedEnv(key)
	}
	//: none: the caller's environment is its own.
	return nil
}

// reservedEnvKey returns the first key of env that begins reservedEnvPrefix,
// compared without regard to case: Windows reads environment names that way,
// and a near-miss on Unix is refused too, so the verdict never depends on the
// platform or on how the caller spelled the prefix.
func reservedEnvKey(env []string) (key string, found bool) {
	//: every entry's key, the part before its first '='.
	for _, kv := range env {
		k := envKey(kv)
		//: long enough to carry the prefix, and carrying it in some case.
		if len(k) >= len(reservedEnvPrefix) && strings.EqualFold(k[:len(reservedEnvPrefix)], reservedEnvPrefix) {
			//: the offending key.
			return k, true
		}
	}
	//: no key of the protocol.
	return "", false
}

// checkPIDEnv refuses a Spec.PIDEnv name the trampoline cannot carry or the
// child could not read: empty, or holding '=', ',' or NUL — INVALID_SPEC —,
// or beginning the spawn protocol's prefix — RESERVED_ENV.
func checkPIDEnv(names []string) error {
	//: each name.
	for _, name := range names {
		//: not a variable's name, or one the comma-joined list would split.
		if name == "" || strings.ContainsAny(name, "=,\x00") {
			//: INVALID_SPEC naming it.
			return wrapInvalidSpec(errs.String("pidenv", name))
		}
		//: the protocol's own.
		if key, found := reservedEnvKey([]string{name}); found {
			//: RESERVED_ENV naming it.
			return wrapReservedEnv(key)
		}
	}
	//: every name settable.
	return nil
}
