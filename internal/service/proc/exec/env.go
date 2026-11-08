package exec

import (
	"strings"
)

// EnvAllow returns the entries of parent whose key is one of keys, in parent's
// order: the environment to hand a child that must see those variables and no
// other. A key that appears more than once keeps its last value, at the place of
// its last occurrence — the value a getenv would read. Keys compare
// case-insensitively on Windows and exactly elsewhere. parent is not modified.
func EnvAllow(parent []string, keys ...string) []string {
	//: keep an entry exactly when its key is named.
	return envFilter(parent, keys, true)
}

// EnvDeny returns the entries of parent whose key is none of keys, in parent's
// order, a repeated key keeping its last value at the place of its last
// occurrence. Keys compare as in EnvAllow. parent is not modified.
func EnvDeny(parent []string, keys ...string) []string {
	//: keep an entry exactly when its key is not named.
	return envFilter(parent, keys, false)
}

// envFilter keeps the entries of parent whose key's membership in keys equals
// keep, dropping every occurrence of a key but its last. The result is a new
// slice, never nil.
func envFilter(parent, keys []string, keep bool) []string {
	out := make([]string, 0, len(parent))
	//: walk in order; an entry survives when no later one repeats its key.
	for i, kv := range parent {
		key := envKey(kv)
		//: the allow / deny decision on the key.
		if namedKey(keys, key) != keep {
			//: filtered out.
			continue
		}
		//: a later entry of the same key wins.
		if repeatedLater(parent[i+1:], key) {
			//: this occurrence is shadowed.
			continue
		}
		out = append(out, kv)
	}
	//: the filtered environment, in parent's order.
	return out
}

// dedupEnv returns env with each variable once: the value and the place of its
// last occurrence, which is what os/exec hands a child of
// append(os.Environ(), "K=v") and what searchPathOf, EnvAllow and EnvDeny read
// already. A child's getenv reads the FIRST entry of a name, so a variable left
// twice in the block would be the one the caller meant to replace. An entry
// without "=" assigns nothing and stays as it is. Names compare as sameVariable
// compares them. The result is a new slice, never nil; env is not modified.
func dedupEnv(env []string) []string {
	last := make(map[string]int, len(env))
	//: where each variable is last assigned.
	for i, kv := range env {
		//: an assignment.
		if strings.Contains(kv, "=") {
			last[variableKey(envKey(kv))] = i
		}
	}
	out := make([]string, 0, len(env))
	//: each entry, unless a later one assigns its variable.
	for i, kv := range env {
		//: no assignment, or the variable's last.
		if !strings.Contains(kv, "=") || last[variableKey(envKey(kv))] == i {
			out = append(out, kv)
		}
	}
	//: one entry per variable, in the caller's order.
	return out
}

// envKey returns the name of one KEY=value entry; an entry without '=' is all
// key. A leading '=' belongs to the name, as os/exec reads it: Windows keeps
// each drive's current directory as "=C:=C:\dir", and "=C:" and "=D:" are two
// variables.
func envKey(kv string) string {
	start := 0
	//: a name opening with "=".
	if strings.HasPrefix(kv, "=") {
		start = 1
	}
	end := strings.IndexByte(kv[start:], '=')
	//: no assignment after the name.
	if end < 0 {
		//: the whole entry.
		return kv
	}
	//: what getenv matches against.
	return kv[:start+end]
}

// namedKey reports whether key is one of keys, compared as the platform
// compares environment variable names (sameVariable).
func namedKey(keys []string, key string) bool {
	//: a linear scan: a list of keys is a handful.
	for _, k := range keys {
		//: the platform's comparison of variable names.
		if sameVariable(k, key) {
			//: named.
			return true
		}
	}
	//: not named.
	return false
}

// repeatedLater reports whether any entry of rest has key.
func repeatedLater(rest []string, key string) bool {
	//: a later occurrence shadows this one.
	for _, kv := range rest {
		//: the same variable, as the platform compares names.
		if sameVariable(envKey(kv), key) {
			//: shadowed.
			return true
		}
	}
	//: the last occurrence.
	return false
}
