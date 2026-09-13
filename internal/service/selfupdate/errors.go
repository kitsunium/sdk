// Package selfupdate — the sentinels this IMPLEMENTATION emits, as opposed to
// the ones the domain contract names.
//
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public here is wire-safe, so none of them names a path, a URL, a host,
// a tag or an asset. That is not squeamishness: the public half is the one
// documented safe to put in a response body, and this package's inputs are a
// release host and a filesystem — every particular it could name came from one
// of the two. What it says instead is the one thing an operator has to take
// away, which is whether the binary they are running was replaced.
//
// Where it happened and what the filesystem said live in Private and Fields.
// They are not lost to the operator: diagnose renders them and
// ExplainUpgradeFailure prints them under the sentence, because a terminal the
// operator owns is not a wire.
package selfupdate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitUsage matches sysexits EX_USAGE (64). A candidate install with no tag is
// a command that was written wrong; nothing about the system is broken.
const exitUsage int = 64

// exitCantCreate matches sysexits EX_CANTCREAT (73). Staging, replacing and an
// authorised escalation all fail because an output file could not be created
// or moved, which is exactly what that status is for.
const exitCantCreate int = 73

var (
	// CandidateTagRequired is returned by DownloadCandidate for an empty tag.
	//
	// There is deliberately no "latest candidate" to fall back to. A
	// prerelease channel has no ordering an updater may assume — rc.2 can be
	// published after rc.3 is withdrawn — so resolving the empty tag would
	// mean this package choosing which unreleased build to install over the
	// running binary.
	CandidateTagRequired = errs.Define(CodeCandidateTagRequired, "CANDIDATE_TAG_REQUIRED",
		"a release candidate is installed by naming its tag, and no tag was named",
		"service/selfupdate: DownloadCandidate received an empty tag; errors.ErrUnsupported is kept as the cause, so anything that matched it before this sentinel existed still matches",
		errs.WithExitCode(exitUsage))

	// ReleaseMetadataUnreadable is returned when the release API answers
	// within its size cap and the body does not decode as release metadata.
	//
	// It is separate from the download sentinels on purpose: the bytes
	// arrived, so no retry policy keyed on a transport failure applies, and
	// the thing to look at is what the host actually sent.
	ReleaseMetadataUnreadable = errs.Define(CodeReleaseMetadataUnreadable, "RELEASE_METADATA_UNREADABLE",
		"the release host's answer could not be read as release metadata",
		"service/selfupdate: a release API response decoded neither as a release nor as a list of them; the fields name the query and, where the query had one, the tag")

	// ArchiveUnreadable is returned when the release archive cannot be
	// buffered or unpacked.
	//
	// Everything under this sentinel happens AFTER the archive's signature
	// and digest have both been verified, which is what makes it a packaging
	// or transfer fault rather than a supply-chain one. Saying so matters:
	// the documented response to an authenticity refusal is "do not install
	// this by any means", and that is the wrong advice for a gzip stream that
	// was truncated on the way in.
	ArchiveUnreadable = errs.Define(CodeArchiveUnreadable, "ARCHIVE_UNREADABLE",
		"the release archive could not be read",
		"service/selfupdate: buffering or unpacking the verified archive failed; the fields name the stage and, for a zip entry, which entry")

	// ExecutablePathUnresolved is returned when the process cannot work out
	// which file on disk it is running from.
	//
	// Nothing is attempted after it. An updater that guesses its own path
	// writes a downloaded binary somewhere nobody asked for.
	ExecutablePathUnresolved = errs.Define(CodeExecutablePathUnresolved, "EXECUTABLE_PATH_UNRESOLVED",
		"the path of the running binary could not be resolved, so nothing was replaced",
		"service/selfupdate: os.Executable or the symlink resolution behind it failed; the fields name which of the two")

	// StagingFailed is returned when the replacement fails BEFORE the rename.
	//
	// The installed binary is byte-for-byte as it was, and the partial
	// staging file has been removed. That is the whole content of this
	// sentinel, and it is why it is not merged with ReplacementFailed: the
	// two differ in exactly the fact an operator needs, which is whether the
	// binary they are about to run is the old one or something in between.
	StagingFailed = errs.Define(CodeStagingFailed, "STAGING_FAILED",
		"the update could not be staged, and the running binary was left untouched",
		"service/selfupdate: creating, writing, closing or chmod-ing the staging file failed; the fields name the step",
		errs.WithExitCode(exitCantCreate))

	// ReplacementFailed is returned when the rename of the staged file over
	// the running executable does not land.
	//
	// The rename is atomic, so this means the old binary is still in place —
	// but unlike StagingFailed it is the step that would have changed it, and
	// the remedy is about the install directory rather than about disk space.
	ReplacementFailed = errs.Define(CodeReplacementFailed, "REPLACEMENT_FAILED",
		"the running binary could not be replaced",
		"service/selfupdate: the rename of the staged file over the running executable failed; the fields name the step",
		errs.WithExitCode(exitCantCreate))

	// ElevationFailed is returned when an escalation the operator DID
	// authorise is refused by the system.
	//
	// It is deliberately not ElevationNotAuthorised, and the gap between them
	// is the remedy: that one is repaired by exporting one variable, and this
	// one is not repaired by exporting anything. Merging them would send
	// somebody who already set the variable to set it again.
	ElevationFailed = errs.Define(CodeElevationFailed, "ELEVATION_FAILED",
		"the elevated replacement was authorised and the system refused it",
		"service/selfupdate: `sudo -n mv` exited non-zero; the fields carry sudo's own output, which names the real blocker far better than the exec error does",
		errs.WithExitCode(exitCantCreate))
)
