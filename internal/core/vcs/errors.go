// Package vcs — the sentinels every implementation of this domain returns.
package vcs

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// RepositoryUnresolved is returned when a path is not inside a readable
	// repository, or when the probe that would have told us failed.
	RepositoryUnresolved = errs.Define(CodeRepositoryUnresolved, "REPOSITORY_UNRESOLVED",
		"the path is not inside a readable repository",
		"core/vcs: the repository probe failed or the path lies outside any repository")

	// CommandFailed is returned when a version-control command exits non-zero.
	// The command line and its stderr are attached as Private: they can name
	// branches, paths and remotes, none of which belongs on a wire-safe surface.
	CommandFailed = errs.Define(CodeCommandFailed, "COMMAND_FAILED",
		"the version-control command failed",
		"core/vcs: the version-control command exited non-zero")

	// PathAbsent is returned when a path does not exist at the requested commit.
	// It is deliberately distinct from a successful read of an empty file: a
	// caller reconstructing history has to tell "deleted" from "emptied".
	PathAbsent = errs.Define(CodePathAbsent, "PATH_ABSENT",
		"the path does not exist at that commit",
		"core/vcs: the requested path is absent from the requested commit's tree")
)
