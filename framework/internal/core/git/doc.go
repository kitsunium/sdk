// Package git — the error-code range owned by this domain (ADR 0005 §Registry).
//
// Package git — the sentinels every implementation of this domain returns.
//
// Package git is the version-control contract: what "the set a branch changed"
// is, and what a resolution of it reports.
//
// The domain is deliberately thin. It names a CHANGED SET — the files, line
// ranges and directories a branch touched relative to its merge-base — and the
// outcome of computing one. It does not model repositories, commits, refs or
// history: the SDK has exactly one implementation, which shells out to the git
// binary (framework/internal/service/git), and a contract broader than that would
// describe nothing real.
//
// There is **no registry**. A registry's key would be a VCS name, and resolving
// one from a config string would let a typo silently swap the implementation
// with every call still succeeding — the same argument ADR 0052 makes for lock.
//
// Package git — the LineRangeValue value type: an inclusive run of changed lines.
package git
