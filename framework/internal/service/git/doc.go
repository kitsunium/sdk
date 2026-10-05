// Package git — the changed set: which files, line ranges and directories a
// branch touched. The concrete implementation of the core coregit.ChangedSet port.
//
// Package git — the compile-time proof that ChangedSetValue still satisfies the
// core port.
//
// Hoisted out of changed_set.go per KTN-IFACE-ASSERT-PLACEMENT: the production
// source carries no purely verificational declarations. Without this assertion a
// method renamed here would only fail at the call site that still expected the
// old name, which may be in another repository.
//
// Package git — the resolver's construction parameters.
//
// Package git — parsing git's unified-diff and name-status output into a
// changed set.
//
// Package git implements the core git contract by shelling out to the git binary.
//
// There is no VCS library dependency: git's own porcelain is the contract, and
// every invocation is hardened against a repository that may be hostile — see
// hardenedGitConfig.
//
// Package git — resolving the git directory that governs a path, once per root.
//
// Package git — what a working tree is at: its head commit, that commit's
// time, and whether tracked files differ from it.
//
// Package git — resolving what a branch changed versus its merge-base with the
// default branch.
//
// Package git — reading a file's content at a specific commit.
package git
