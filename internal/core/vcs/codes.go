// Package vcs — the error-code range owned by this domain (ADR 0005 §Registry).
package vcs

import "github.com/kitsunium/sdk/internal/kernel/errs"

// CodeRepositoryUnresolved identifies a path that is not inside a repository the
// implementation can read, or a repository probe that failed outright.
const CodeRepositoryUnresolved errs.Code = 0x00_02_21_01 // 0.2.33.1

// CodeCommandFailed identifies a version-control command that exited non-zero.
// The stderr tail travels in Private, never in Public.
const CodeCommandFailed errs.Code = 0x00_02_21_02 // 0.2.33.2

// CodePathAbsent identifies a path that does not exist at the requested commit,
// which is distinct from a path that exists and is empty.
const CodePathAbsent errs.Code = 0x00_02_21_03 // 0.2.33.3
