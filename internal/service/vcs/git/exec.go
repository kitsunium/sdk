// Package git implements the vcs domain by shelling out to the git binary.
//
// There is no VCS library dependency: git's own porcelain is the contract, and
// every invocation is hardened against a repository that may be hostile — see
// hardenedGitConfig.
package git

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// hardenedGitConfig neutralises the git configuration keys that make a git
// child process execute an arbitrary command chosen by the repository it is
// pointed at.
//
// This matters because the MCP daemon runs git against whatever repository the
// user opened, and `.git/config` travels with a clone. A planted key therefore
// runs code as the daemon user on an operation the user believes is read-only.
//
// Both entries below were demonstrated, not assumed. Against a repository with
// the key set to a script, the daemon's own invocations
// (`diff -U0 -M -C`, `status --porcelain`) executed it 5 times; with these two
// overrides in place, 0 times:
//
//   - core.fsmonitor: git runs it to enumerate changed paths, on status and diff.
//     Disabled here by setting it empty; -c beats every config file, so the
//     target repository cannot re-enable it.
//   - diff.external: git runs it in place of its own diff implementation.
//     This one is NOT disabled with -c. Setting diff.external to the empty
//     string does not disable it — git tries to execute "" and aborts with
//     `cannot run : No such file or directory` / `external diff died`, which
//     breaks diff outright. The documented lever is the --no-ext-diff flag,
//     injected per-subcommand by extDiffGuard below.
//
// Deliberately NOT included, to avoid hardening against nothing:
//   - core.pager: tested, does not execute — git detects the non-TTY and skips paging.
//   - diff.<driver>.textconv: did not fire on any invocation this package makes.
//   - core.hooksPath: none of these subcommands run hooks.
//   - credential.helper / core.sshCommand / protocol.*: network operations only,
//     and this package performs none.
//
// A third group was added after the first two, and it executes nothing: the
// four keys that choose the a//b/ prefixes of a unified-diff header. They do
// not run code, they make the parser read a path that is not the file's, and
// the symptom is the one this package exists to prevent — a changed line that
// answers false. Measured against a repository carrying each key, on the diff
// this package actually runs:
//
//	diff.srcPrefix=SRC/ + diff.dstPrefix=DST/  ContainsFile true, ContainsLine FALSE
//	diff.mnemonicPrefix=true (index/worktree)  ContainsFile true, ContainsLine FALSE
//	diff.noprefix=true                         both true — "a.txt" survives the
//	                                           "b/" strip by coincidence, so it
//	                                           is neutralised for the coincidence
//	                                           rather than for the symptom
//
// ContainsFile survives all three because the NUL-separated name-status pass
// carries no prefixes at all; only the line ranges are lost. diff.noprefix wins
// over diff.srcPrefix/diff.dstPrefix, so pinning the two prefixes is not enough
// on its own and all four are set.
var (
	hardenedGitConfig []string = []string{
		//: core.quotepath=off makes git print non-ASCII path bytes verbatim
		//: instead of c-quoting them ("caf\303\251.go" wrapped in double
		//: quotes), which would defeat the a//b/ prefix-stripping and the .go
		//: suffix check in the diff/ls-files parsers and silently drop the file
		//: from the changed-set. Control characters (tab, newline, quote) are
		//: still quoted — the NUL-separated name-status/-z passes cover those
		//: residual cases.
		"-c", "core.quotepath=off",
		"-c", "core.fsmonitor=",
		//: No subcommand this package runs consults a hook — diff, status,
		//: ls-files, rev-parse and merge-base are all read-only. It is set
		//: anyway so the guarantee does not depend on that list staying
		//: read-only: the next person to add a subcommand here should not have
		//: to re-derive which ones execute hooks. internal/worktree, which does
		//: commit, needs it for real.
		"-c", "core.hooksPath=",
		//: The four keys that choose a unified-diff header's path prefixes.
		//: They execute nothing; they make "+++ b/x.go" arrive as "+++ w/x.go"
		//: or "+++ DST/x.go", which the b/ strip leaves intact, so the line
		//: ranges are filed under a path no caller will ever query. Pinned to
		//: git's documented defaults rather than disabled, because "a/" and
		//: "b/" are what the parser is written against. diff.noprefix is set
		//: first in spirit and last in effect: it overrides the two explicit
		//: prefixes, so neutralising them without it changes nothing.
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"-c", "diff.srcPrefix=a/",
		"-c", "diff.dstPrefix=b/",
	}

	// extDiffSubcommands lists the git subcommands that honour diff.external
	// and therefore need the --no-ext-diff flag. Other subcommands reject the
	// flag outright, so it cannot simply be appended to every invocation.
	extDiffSubcommands map[string]bool = map[string]bool{
		"diff": true,
		"show": true,
		"log":  true,
	}
)

// extDiffGuard returns args with --no-ext-diff inserted directly after the
// subcommand when that subcommand honours diff.external, and unchanged
// otherwise.
//
// It takes the git subcommand and its arguments, and returns them with the
// flag injected when applicable.
func extDiffGuard(args []string) []string {
	//: Nothing to guard without a subcommand, and only diff-producing
	//: subcommands accept (or need) the flag.
	if len(args) == 0 || !extDiffSubcommands[args[0]] {
		//: Return the invocation untouched.
		return args
	}
	//: Insert immediately after the subcommand, before any other argument.
	guarded := make([]string, 0, len(args)+1)
	guarded = append(guarded, args[0], "--no-ext-diff")
	guarded = append(guarded, args[1:]...)
	//: Return the computed result to the caller.
	return guarded
}

// runGitOutput executes a git subcommand inside repo and returns trimmed
// stdout. stderr is captured and folded into the returned error so a failed
// invocation is debuggable without a silent discard.
//
// Every invocation carries hardenedGitConfig so a hostile `.git/config` in the
// target repository cannot turn a read-only query into code execution.
func runGitOutput(ctx context.Context, repo string, args ...string) (gitOutput string, err error) {
	//: Config hardening plus path-quoting, centralised so every parsed git
	//: invocation in this package benefits. See hardenedGitConfig.
	full := append(append([]string{"-C", repo}, hardenedGitConfig...), extDiffGuard(args)...)
	cmd := exec.CommandContext(ctx, "git", full...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, runErr := cmd.Output()
	//: A non-zero git exit is expected for several probes (no origin/HEAD,
	//: unborn branch); surface stderr so the caller can decide fallback vs error.
	if runErr != nil {
		//: The subcommand and git's stderr go in Private: a branch name, a
		//: path or a remote URL is not wire-safe. Public stays the sentinel's.
		return "", errs.Wrap(runErr, errs.WrapParams{
			Code:    corevcs.CodeCommandFailed,
			Reason:  "COMMAND_FAILED",
			Public:  "the version-control command failed",
			Private: "service/vcs/git: git " + strings.Join(args, " ") + ": " + strings.TrimSpace(stderr.String()),
		})
	}

	//: Return trimmed stdout to the caller.
	return strings.TrimSpace(string(stdout)), nil
}

// runGitBlob is runGitOutput without the trim: it returns stdout verbatim, and
// hands the failure back unwrapped so the caller can decide which refusal it is.
//
// The trim is right for every probe in this package — a SHA, a ref name, a
// top-level path all arrive with a trailing newline nobody wants. It is wrong
// for a file's contents, where leading and trailing whitespace is the file's,
// and where a whitespace-only blob would otherwise come back empty.
//
// The error is NOT typed here, unlike everywhere else in this file. A blob read
// fails for two reasons a caller has to tell apart — the commit is unreachable,
// or the path is absent from it — and only the caller knows which probes
// separate them. It returns the exec error and git's stderr tail, and
// blobFailure below turns that pair into whichever sentinel applies.
func runGitBlob(ctx context.Context, repo string, args ...string) (blob, stderrTail string, err error) {
	//: Same hardening as every other invocation; only the trim differs.
	full := append(append([]string{"-C", repo}, hardenedGitConfig...), extDiffGuard(args)...)
	cmd := exec.CommandContext(ctx, "git", full...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, runErr := cmd.Output()
	//: A non-zero exit is the caller's to classify, so pass it through whole.
	if runErr != nil {
		//: Raw failure plus the stderr tail the caller will put in Private.
		return "", strings.TrimSpace(stderr.String()), runErr
	}

	//: Verbatim — the bytes are the file's, not a field to be tidied.
	return string(stdout), "", nil
}

// blobFailure types a failed blob read as CommandFailed, with the subcommand
// and git's stderr in Private where a branch name or a path is not wire-visible.
func blobFailure(cause error, stderrTail string, args ...string) error {
	//: The same typed refusal runGitOutput produces, built where the caller
	//: has already decided this is not an absent path.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corevcs.CodeCommandFailed,
		Reason:  "COMMAND_FAILED",
		Public:  "the version-control command failed",
		Private: "service/vcs/git: git " + strings.Join(args, " ") + ": " + stderrTail,
	})
}

// gitProbe runs a git subcommand purely for its success/failure signal,
// discarding stdout. Used by boolean probes (is-shallow, ref existence) where
// the exit code is the answer.
func gitProbe(ctx context.Context, repo string, args ...string) bool {
	_, err := runGitOutput(ctx, repo, args...)
	//: Success (nil error) means the probe condition holds.
	return err == nil
}
