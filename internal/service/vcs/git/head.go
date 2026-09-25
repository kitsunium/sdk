// Package git — what a working tree is at: its head commit, that commit's
// time, and whether tracked files differ from it.
package git

import (
	"context"
	"strconv"
	"strings"
	"time"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// committerPrefix opens the header line of a commit object that carries the
// committer's identity and date.
const committerPrefix string = "committer "

// tzDigits is the length of a git timezone offset without its sign: "0200".
const tzDigits int = 4

// secondsPerMinute and minutesPerHour turn an "hhmm" offset into seconds.
const (
	secondsPerMinute int = 60
	minutesPerHour   int = 60
)

// decimalBase and int64Bits are the arguments strconv.ParseInt needs to read
// a Unix timestamp.
const (
	decimalBase int = 10
	int64Bits   int = 64
)

// HeadValue is what a working tree is at: the commit HEAD names, that
// commit's time, and whether any tracked file differs from it.
//
// It is the engine's own value (ADR 0074): the vcs port answers what a branch
// CHANGED and deliberately models no commit, so a second implementation of
// that port would have no reason to produce this.
type HeadValue struct {
	// Time is the commit's committer date, in the offset it was recorded
	// with — what `git log --format=%cI` prints.
	Time time.Time
	// Revision is the full object name of the commit HEAD resolves to.
	Revision string
	// Modified reports that a TRACKED file differs from HEAD, staged or not.
	// An untracked file does not count: it is not part of what the commit
	// describes, and a build stamp that turned dirty whenever an editor left
	// a swap file would say nothing. This is narrower than the vcs.modified
	// Go stamps into a binary, which counts untracked files too.
	Modified bool
}

// Head reports what the working tree containing dir is at: the commit HEAD
// names, its time, and whether tracked files differ from it.
//
// It runs three read-only git commands, all through the hardened runner, so a
// hostile .git/config cannot turn the query into command execution the way it
// can a bare `git status` (see hardenedGitConfig). The commit time is read
// from the raw commit object rather than through `git log`, because log
// honours log.showSignature and would hand a planted gpg.program a signature
// to "verify". The status runs with --no-optional-locks, so asking never
// takes the index lock a concurrent `git commit` in the same tree needs.
//
// It caches nothing: modified is the one fact here that changes without a
// commit. A caller asking often keeps its own answer.
//
// A dir outside any repository — or one that does not exist, or a machine
// with no git — is RepositoryUnresolved. A repository whose HEAD names no
// commit yet (an unborn branch), a bare repository, and a failed read are
// CommandFailed, with the subcommand and git's stderr in Private.
func Head(ctx context.Context, dir string) (head HeadValue, err error) {
	revision, revErr := runGitOutput(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	//: the one failure worth telling apart: no repository at all.
	if revErr != nil {
		//: RepositoryUnresolved or CommandFailed, decided by a probe.
		return HeadValue{}, headFailure(ctx, dir, revErr)
	}
	commit, catErr := runGitOutput(ctx, dir, "cat-file", "commit", revision)
	//: the object HEAD names could not be read.
	if catErr != nil {
		//: CommandFailed, as the runner typed it.
		return HeadValue{}, catErr
	}
	at, parsed := committerTime(commit)
	//: a commit object without a readable committer line is corrupt.
	if !parsed {
		//: CommandFailed: git answered, but not with a commit it can describe.
		return HeadValue{}, errs.Wrap(corevcs.CommandFailed, errs.WrapParams{},
			errs.String("revision", revision), errs.String("problem", "no readable committer line"))
	}
	status, statusErr := runGitOutput(ctx, dir,
		"--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	//: a tree whose status cannot be read is not reported clean — that would
	//: be a claim git refused to make.
	if statusErr != nil {
		//: CommandFailed, as the runner typed it.
		return HeadValue{}, statusErr
	}
	//: one line per tracked file that differs; none means clean.
	return HeadValue{Revision: revision, Time: at, Modified: status != ""}, nil
}

// headFailure decides which refusal a failed HEAD resolution is.
//
// It is decided by a probe, not by reading git's stderr, whose wording is
// localised and unversioned — the same rule ShowFile follows. The probe runs
// only on the failure path.
func headFailure(ctx context.Context, dir string, cause error) error {
	//: a directory git can find a repository from, whose HEAD is nonetheless
	//: not a commit: an unborn branch, typically.
	if gitProbe(ctx, dir, "rev-parse", "--git-dir") {
		//: the runner's CommandFailed, subcommand and stderr in Private.
		return cause
	}
	//: no repository, no directory, or no git: all the same instruction to a
	//: caller — go without.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corevcs.CodeRepositoryUnresolved,
		Reason:  "REPOSITORY_UNRESOLVED",
		Public:  "the path is not inside a readable repository",
		Private: "service/vcs/git: no repository could be resolved from the directory",
	})
}

// committerTime reads the committer date out of a raw commit object: the
// header line "committer Name <email> 1727270400 +0200", whose last two fields
// are the Unix time and the offset it was recorded in.
//
// The name is free text and the email is between angle brackets, so the two
// fields are taken from after the LAST '>' rather than by splitting on spaces.
// Header lines end at the first empty line; a continuation line (a signature,
// a mergetag) starts with a space and never with "committer ".
func committerTime(commit string) (at time.Time, ok bool) {
	//: the header only: the message below it may contain anything at all.
	header, _, _ := strings.Cut(commit, "\n\n")
	//: one header line at a time.
	for line := range strings.SplitSeq(header, "\n") {
		identity, found := strings.CutPrefix(line, committerPrefix)
		//: not the committer line.
		if !found {
			continue
		}
		//: the first committer line is the only one git writes.
		return parseSignature(identity)
	}
	//: no committer line at all.
	return time.Time{}, false
}

// parseSignature reads "Name <email> seconds ±hhmm" into a time in its
// recorded offset.
func parseSignature(identity string) (at time.Time, ok bool) {
	_, stamp, closed := strings.CutLast(identity, ">")
	//: no email: not a signature git writes.
	if !closed {
		//: unreadable.
		return time.Time{}, false
	}
	fields := strings.Fields(stamp)
	//: exactly the timestamp and the offset.
	if len(fields) != 2 {
		//: unreadable.
		return time.Time{}, false
	}
	seconds, secondsErr := strconv.ParseInt(fields[0], decimalBase, int64Bits)
	offset, offsetOK := parseOffset(fields[1])
	//: both halves must read.
	if secondsErr != nil || !offsetOK {
		//: unreadable.
		return time.Time{}, false
	}
	//: the instant, shown in the offset it was recorded with, as %cI prints it.
	return time.Unix(seconds, 0).In(time.FixedZone("", offset)), true
}

// parseOffset reads a git timezone offset, "+0200" or "-0530", into seconds
// east of UTC.
func parseOffset(zone string) (seconds int, ok bool) {
	//: a sign and four digits, nothing else.
	if len(zone) != tzDigits+1 || (zone[0] != '+' && zone[0] != '-') {
		//: unreadable.
		return 0, false
	}
	hours, hoursErr := strconv.Atoi(zone[1:3])
	minutes, minutesErr := strconv.Atoi(zone[3:])
	//: digits only; strconv accepts a sign, which is already consumed.
	if hoursErr != nil || minutesErr != nil || hours < 0 || minutes < 0 || minutes >= minutesPerHour {
		//: unreadable.
		return 0, false
	}
	seconds = (hours*minutesPerHour + minutes) * secondsPerMinute
	//: west of UTC is negative.
	if zone[0] == '-' {
		//: negated.
		return -seconds, true
	}
	//: east of UTC.
	return seconds, true
}
