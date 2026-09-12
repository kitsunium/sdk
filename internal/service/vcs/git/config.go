// Package git — the resolver's construction parameters.
package git

// Config is what Resolve needs to know: where the repository is, and which files
// the caller considers part of the change.
//
// Both fields have a meaningful zero and neither is refused (ADR 0031): an empty
// Root resolves from the process working directory, which is what a tool run
// inside the repository wants, and a nil Include admits every file, which is the
// safe direction — a filter defaulting to "exclude" would under-report a diff.
type Config struct {
	// Root is a directory inside the repository to resolve from. Empty resolves
	// from the process working directory.
	Root string
	// Include decides which files belong in the changed set. Nil admits all.
	//
	// This is where a caller states the policy the source implementation had
	// hard-coded: a Go linter passes a test for a ".go" suffix and a
	// generated-file probe; a documentation tool passes something else; a tool
	// that wants everything passes nil.
	Include IncludeFunc
}

// IncludeFunc reports whether the file at absPath belongs in the changed set.
//
// It replaces what the source implementation hard-coded: a ".go" suffix test
// plus a generated-file probe. Those are one caller's policy, not the domain's —
// a changed set over Markdown, or one that keeps generated files, is just as
// legitimate — so the policy is the caller's to state.
//
// A nil IncludeFunc includes everything. That is the safe default in the ADR
// 0031 sense: the failure mode of including too much is a wider scope, while a
// default that silently dropped files would under-report a diff, which is the
// one answer this package must never give.
type IncludeFunc func(absPath string) bool
