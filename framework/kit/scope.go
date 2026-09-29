// Package kit — scopes: the parts of a singleton's lock or of a socket's
// path that are only known at the start — the user, the executable, a
// configuration directory —, so one declaration keeps one process per user,
// per installed copy or per configuration.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// CodeScopeUnknown reports a scope whose value could not be read at the
// start: no current user, no executable path, no configuration directory.
const CodeScopeUnknown errs.Code = ikit.CodeScopeUnknown

var (
	// PerUID is the current user: one process per user of the machine (its
	// UID on Unix, its SID on Windows).
	PerUID Scope = ikit.PerUID

	// PerExecutable is the running executable's path, links resolved: one
	// process per installed copy of the binary.
	PerExecutable Scope = ikit.PerExecutable

	// PerConfigDir is the user's configuration directory (os.UserConfigDir):
	// one process per configuration home.
	PerConfigDir Scope = ikit.PerConfigDir
)

// Scope is one part of a path computed at the start: the user, the
// executable, a configuration directory. Two processes share a singleton or
// a socket when every scope gives them the same value.
type Scope = ikit.ScopeValue

// PerEnv is the environment variable name, else fallback when it is unset
// or empty; a leading "~/" in either is the user's home directory. The
// status line's is PerEnv("CLAUDE_CONFIG_DIR", "~/.claude"): one daemon per
// Claude configuration.
func PerEnv(name, fallback string) Scope {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.PerEnv(name, fallback)
}

// ScopeKey is the name the scopes give in this process: 16 hex digits of the
// SHA-256 of their values. A client computes the key the daemon computed
// from the same declaration, and so finds its lock or its socket.
func ScopeKey(scopes ...Scope) (string, error) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.ScopeKey(scopes...)
}
