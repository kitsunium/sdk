// Package git — reading a file's content at a specific commit.
package git

import (
	"context"
)

// ShowFile returns the content of relPath at commit sha via `git show`.
// relPath is resolved relative to repoRoot (the "./" prefix pins git's
// path resolution to the -C directory instead of the repository top level).
// The error carries git's stderr; a path absent at that commit is an error,
// letting callers distinguish "absent" from "empty".
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (content string, err error) {
	//: git show <sha>:./<path> prints the blob verbatim (trimmed by the runner).
	return runGitOutput(ctx, repoRoot, "show", sha+":./"+relPath)
}
