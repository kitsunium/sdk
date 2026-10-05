package kit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// scopeKeyBytes is how many bytes of the scopes' SHA-256 name a lock or a
// socket: 16 hex digits keep a socket path under sun_path.
const scopeKeyBytes int = 8

// ScopeValue is one part of a path computed at the start: the user, the
// executable, a configuration directory. Two processes share a singleton or
// a socket when every scope gives them the same value.
type ScopeValue struct {
	// label names the scope in the graph: "uid", "executable", "env-<name>".
	label string
	// read is the scope's value in this process.
	read func() (string, error)
}

var (
	// PerUID is the current user: one process per user of the machine (its
	// UID on Unix, its SID on Windows).
	PerUID = ScopeValue{label: "uid", read: currentUser}

	// PerExecutable is the running executable's path, links resolved: one
	// process per installed copy of the binary.
	PerExecutable = ScopeValue{label: "executable", read: executablePath}

	// PerConfigDir is the user's configuration directory (os.UserConfigDir):
	// one process per configuration home.
	PerConfigDir = ScopeValue{label: "config-dir", read: os.UserConfigDir}
)

// PerEnv is the environment variable name, else fallback when it is unset
// or empty; a leading "~/" in either is the user's home directory. The
// status line's is PerEnv("CLAUDE_CONFIG_DIR", "~/.claude"): one daemon per
// Claude configuration.
func PerEnv(name, fallback string) ScopeValue {
	label := "env-" + strings.ReplaceAll(strings.ToLower(name), "_", "-")
	return ScopeValue{label: label, read: func() (string, error) {
		v := os.Getenv(name)
		if v == "" {
			v = fallback
		}
		return expandHome(v)
	}}
}

// ScopeKey is the name the scopes give in this process: 16 hex digits of the
// SHA-256 of their values. A client computes the key the daemon computed
// from the same declaration, and so finds its lock or its socket.
func ScopeKey(scopes ...ScopeValue) (string, error) {
	h := sha256.New()
	for _, s := range scopes {
		v, err := s.read()
		if err != nil {
			return "", failure(CodeScopeUnknown, "SCOPE_UNKNOWN", "a scope's value could not be read", nil,
				errs.String("scope", s.label), errs.String("cause", err.Error()))
		}
		h.Write([]byte(s.label + "=" + v + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil)[:scopeKeyBytes]), nil
}

// scopeLabel names scopes in the graph: "per-uid-executable".
func scopeLabel(scopes []ScopeValue) string {
	labels := make([]string, 0, len(scopes)+1)
	labels = append(labels, "per")
	for _, s := range scopes {
		labels = append(labels, s.label)
	}
	return strings.Join(labels, "-")
}

// executablePath is the running executable, links resolved.
func executablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(p string) (string, error) {
	rest, found := strings.CutPrefix(p, "~/")
	if !found && p != "~" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
