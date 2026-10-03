// Package logfile is the hardened open both file sinks share: the append-only
// sink in internal/service/observe/logger/sink/file and the rotating one in
// internal/service/observe/logger/writer/rotfile, which re-runs it on every reopen after a
// rotation. A path whose final component is a symbolic link is refused twice —
// by an os.Lstat BEFORE the open (policy) and by O_NOFOLLOW AT the open where
// the kernel has it (the TOCTOU window the check leaves) — and both refusals
// name the indirection the same way, so an operator reading one line can tell
// a planted link from a full disk (CWE-59).
//
// Both sinks carried a copy of all of it; the copies differed in nothing but
// their error code and their wording (OPEN_FAILED 0.3.14.* for the sink,
// ROT_FILE_OPEN_FAILED for rotfile). Those are what this package does not own:
// a sink hands it a RefusalSpec carrying its two wraps, and every refusal leaves
// under that sink's code, in its words. It declares no code.
package logfile

import (
	"os"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// KindSymlink is the value of the "kind" field both symlink refusals carry —
// the policy one in RefuseSymlink and the kernel one ExplainOpenFailure
// diagnoses. One sentinel per sink serves every open failure, so the field is
// what lets a consumer tell "someone put a link at my log path" apart from a
// full disk, at whichever of the two points caught it.
const KindSymlink string = "symlink"

// The two field keys a refusal carries. Neither ever carries file content.
const (
	pathFieldKey string = "path"
	kindFieldKey string = "kind"
)

// RefusalSpec is one sink's error vocabulary for the hardened open: the wrap a
// policy refusal leaves under, and the wrap a failed os.OpenFile does. Each
// carries the sink's own code, reason and wording, and a sink declares its
// RefusalSpec once, as a package-level value.
type RefusalSpec struct {
	// Symlink wraps the policy refusal: the final component is already a
	// symbolic link, so the open is never attempted.
	Symlink errs.WrapParams
	// Open wraps a failed os.OpenFile — the kernel refusing a link planted
	// after the check among the causes, and a full disk among the others.
	Open errs.WrapParams
}

// Open refuses a pre-existing symlink at path, then opens it for appending —
// O_APPEND|O_CREATE|O_WRONLY, plus O_NOFOLLOW on every Unix — creating it with
// perm. It is the single hardening entry point, so the checks run identically
// at a sink's construction AND on every reopen after a rotation: no path into
// a log file is weaker than another.
//
// The two refusals are not redundant. RefuseSymlink is a check and therefore
// has a window after it; openFlags carries O_NOFOLLOW on every Unix so the
// kernel refuses inside that window (open_flags_unix.go, and
// open_flags_other.go for the platforms where the check is all there is).
// Neither covers a symbolic link at a PARENT component; each sink's "do NOT
// place the log file under an attacker-writable directory" pre-condition does.
//
// The caller owns the returned descriptor.
func Open(path string, perm os.FileMode, refusals *RefusalSpec) (file *os.File, err error) {
	//: reject a pre-existing symlink before OpenFile can follow it.
	if serr := RefuseSymlink(path, &refusals.Symlink); serr != nil {
		//: surface the sink's hardening refusal so HasCode introspection works.
		return nil, serr
	}
	//: open with O_NOFOLLOW where the platform has it, so a symlink planted
	//: between the Lstat check and this call fails the open rather than
	//: silently redirecting the sink to a file someone else chose.
	f, oerr := os.OpenFile(path, openFlags, perm)
	//: wrap os errors via errs.Wrap so errors.Is still catches the cause.
	if oerr != nil {
		//: the sink's open sentinel, plus what an Lstat can still say.
		return nil, ExplainOpenFailure(path, oerr, &refusals.Open)
	}
	//: the caller owns the descriptor.
	return f, nil
}

// RefuseSymlink returns the sink's policy refusal when path's final component
// is a symbolic link, and nil otherwise. os.Lstat does NOT traverse the final
// component, so a link that is already there is caught before OpenFile can
// follow it; an absent path or a stat error passes through, so the OpenFile
// that follows surfaces the real diagnostic uniformly.
func RefuseSymlink(path string, refusal *errs.WrapParams) error {
	//: Lstat does NOT follow the final component.
	info, lerr := os.Lstat(path)
	//: pass through when stat fails OR the target is not a link.
	if lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		//: happy path — hand control back to OpenFile.
		return nil
	}
	//: path resolved to a symlink → reject by policy, carrying the same
	//: "kind" field the kernel-refused path carries so a consumer filtering on
	//: it sees both enforcement points and not just the rarer one.
	return errs.Wrap(nil, *refusal, errs.String(pathFieldKey, path), errs.String(kindFieldKey, KindSymlink))
}

// ExplainOpenFailure wraps a failed open under the sink's open refusal and
// adds what an os.Lstat can still say about path.
//
// That Lstat is DIAGNOSIS and never the decision. The refusal was already taken
// one line earlier, by the kernel, so a planter who removes the link between
// the two calls changes which field this error carries and cannot change
// whether the open was refused. It is the opposite situation from
// RefuseSymlink, which runs BEFORE the open and is a check the flag exists to
// back up — the same distinction internal/service/lock draws in
// classifyOpenFailure (ADR 0082 §D4).
//
// The errno is deliberately not consulted. O_NOFOLLOW reports a planted link as
// ELOOP on linux, openbsd and darwin, EMLINK on freebsd and dragonfly, and
// EFTYPE on netbsd; a three-value table across six kernels is the shape of
// thing that is wrong on the seventh, and there is nothing to branch on anyway
// since both refusals share one sentinel per sink.
func ExplainOpenFailure(path string, openErr error, refusal *errs.WrapParams) error {
	fields := []errs.FieldValue{errs.String(pathFieldKey, path)}
	info, serr := os.Lstat(path)
	//: an indirection sits there now, so the kernel declining to traverse it
	//: is the likeliest reading of the failure — say so, without asking which
	//: errno said it.
	if serr == nil && info.Mode()&os.ModeSymlink != 0 {
		//: same sentinel as any other open failure; only the field differs.
		fields = append(fields, errs.String(kindFieldKey, KindSymlink))
	}
	//: the sink's open refusal, the cause on the trail.
	return errs.Wrap(openErr, *refusal, fields...)
}
