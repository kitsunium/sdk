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
//
// The content is returned VERBATIM. It deliberately does not go through
// runGitOutput, whose trim is right for a SHA or a ref name and wrong for a
// file: leading and trailing whitespace is the file's own, and a
// whitespace-only blob would otherwise come back indistinguishable from empty.
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (content string, err error) {
	//: git show <sha>:./<path> prints the blob; runGitBlob keeps it verbatim.
	return runGitBlob(ctx, repoRoot, "show", sha+":./"+relPath)
}
