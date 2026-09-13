// Internal tests for the public/private split itself — the property the
// errs.Wrap conversion of this package exists to establish, and the one a
// suite that only asserts on err.Error() cannot see.
//
// A mechanical substitution of fmt.Errorf for errs.Wrap passes every existing
// test in this package while putting a release tag, an install path and an
// environment variable name straight back into the wire-safe half. These tests
// are what makes that fail instead.
package selfupdate

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// whole renders both halves of err: the public sentence a caller and a wire
// see, then the diagnostic detail only a log and the operator's terminal see.
//
// Tables that mix the two kinds of expectation assert against this. Which half
// a given particular landed in is not left to them — TestNoParticularReaches
// ThePublicSentence answers that question once, for all of them.
func whole(err error) string {
	//: the separator is not in either half, so a want string cannot match
	//: across the seam by accident.
	return err.Error() + " ||| " + diagnose(err)
}

// namedSentinel is one row of the two sentinel tables below: a sentinel and
// the name a failure should report it by.
//
// A slice rather than a map[string]*errs.Error, so the subtests run in the
// order they are written rather than in Go's randomised map order — which is
// what makes a failure reproducible from the log alone.
type namedSentinel struct {
	name string
	err  *errs.Error
}

// TestNoParticularReachesThePublicSentence is the guard on the split.
//
// Every error below is built with a real particular in it — a release tag, an
// asset name, an install path, an environment variable, a host's own words.
// None of them may appear in err.Error(), which is documented wire-safe and is
// what a caller is free to put in a response body; every one of them must
// appear in diagnose(err), or the context was not moved but lost.
func TestNoParticularReachesThePublicSentence(t *testing.T) {
	t.Parallel()

	const (
		tag    = "v9.9.9-rc.7"
		path   = "/opt/private-tooling/bin/widget"
		secret = "connection reset by 10.1.2.3"
	)

	tests := []struct {
		name string
		err  error
		// particulars must be absent from Error() and present in diagnose().
		particulars []string
	}{
		{
			name:        "a refused candidate names its tag nowhere public",
			err:         refuse(coreupd.CandidateNotFound, errs.String("tag", tag)),
			particulars: []string{tag},
		},
		{
			name: "a checksum mismatch publishes neither digest nor asset",
			err: refuse(coreupd.ChecksumMismatch,
				errs.String("asset", "widget_linux_amd64.tar.gz"),
				errs.String("tag", tag),
				errs.String("manifest_digest", "deadbeef")),
			particulars: []string{tag, "widget_linux_amd64.tar.gz", "deadbeef"},
		},
		{
			name: "a refused elevation publishes neither the path nor the variable",
			err: refuse(coreupd.ElevationNotAuthorised,
				errs.String("path", path),
				errs.String("opt_in_env", "WIDGET_ALLOW_SUDO")),
			particulars: []string{path, "WIDGET_ALLOW_SUDO"},
		},
		{
			name:        "an unresolvable path publishes the path in neither half but the field",
			err:         classify(ExecutablePathUnresolved, os.ErrNotExist, errs.String("path", path)),
			particulars: []string{path},
		},
		{
			name:        "a transport failure publishes nothing the host said",
			err:         classify(coreupd.DownloadFailed, errors.New(secret), errs.String("stage", "get")),
			particulars: []string{secret},
		},
		{
			name: "a redirect refusal publishes neither the target nor its scheme",
			err: refuse(coreupd.InsecureRedirect,
				errs.String("scheme", "http"),
				errs.String("url", "http://evil.example/payload")),
			particulars: []string{"http://evil.example/payload"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			public := tc.err.Error()
			detail := diagnose(tc.err)
			//: Each particular is checked on both sides, because either
			//: failure is a defect: leaking it is the one this guards, and
			//: losing it makes the error useless to whoever must act on it.
			for _, particular := range tc.particulars {
				//: the wire-safe half must not carry it.
				if strings.Contains(public, particular) {
					t.Errorf("err.Error() = %q leaks %q", public, particular)
				}
				//: and the diagnostic half must.
				if !strings.Contains(detail, particular) {
					t.Errorf("diagnose(err) = %q lost %q", detail, particular)
				}
			}
		})
	}
}

// TestEveryPublicSentenceIsWireSafe pins the two structural rules ADR 0005
// puts on a Public message against every sentinel THIS package defines, so a
// new one cannot be added with a newline or a paragraph in it.
//
// errs.Define already panics at init on both, which is a strong guard and a
// late one: the panic fires when the package is loaded, which in a consumer's
// binary is at start-up rather than in this repository's CI.
func TestEveryPublicSentenceIsWireSafe(t *testing.T) {
	t.Parallel()

	const maxPublic int = 120

	sentinels := []namedSentinel{
		{"CandidateTagRequired", CandidateTagRequired},
		{"ReleaseMetadataUnreadable", ReleaseMetadataUnreadable},
		{"ArchiveUnreadable", ArchiveUnreadable},
		{"ExecutablePathUnresolved", ExecutablePathUnresolved},
		{"StagingFailed", StagingFailed},
		{"ReplacementFailed", ReplacementFailed},
		{"ElevationFailed", ElevationFailed},
	}
	for _, tc := range sentinels {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			public := tc.err.Public()
			//: A newline breaks a single-line log format and any header.
			if strings.ContainsAny(public, "\r\n") {
				t.Errorf("%s public = %q, want no line break", tc.name, public)
			}
			//: The rune bound is the SDK's, and it is on runes not bytes.
			if runes := len([]rune(public)); runes > maxPublic {
				t.Errorf("%s public is %d runes, want <= %d", tc.name, runes, maxPublic)
			}
			//: A private half that says nothing leaves the error with no
			//: diagnosis at all once the public half stops carrying one.
			if strings.TrimSpace(tc.err.Private()) == "" {
				t.Errorf("%s has an empty private half", tc.name)
			}
		})
	}
}

// TestClassifyCarriesTheSentinelVerbatim is the check that makes classify's
// read-it-from-the-sentinel approach safe.
//
// The alternative — restating Code, Reason, Public, Private and the exit
// status at each call site, which is what the sibling service packages do for
// their one or two sites — is four strings copied by hand with nothing
// comparing the copy to the original. This package reaches DownloadFailed from
// twelve sites in five files, eight through classify. This test is why none of
// them can drift.
func TestClassifyCarriesTheSentinelVerbatim(t *testing.T) {
	t.Parallel()

	cause := errors.New("the medium said no")

	sentinels := []namedSentinel{
		{"DownloadFailed", coreupd.DownloadFailed},
		{"SignatureInvalid", coreupd.SignatureInvalid},
		{"ArchiveUnreadable", ArchiveUnreadable},
		{"StagingFailed", StagingFailed},
		{"ReplacementFailed", ReplacementFailed},
		{"ElevationFailed", ElevationFailed},
		{"ExecutablePathUnresolved", ExecutablePathUnresolved},
		{"ReleaseMetadataUnreadable", ReleaseMetadataUnreadable},
		{"CandidateTagRequired", CandidateTagRequired},
	}
	for _, tc := range sentinels {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tc.err, cause)
			//: Identity: a caller matching the sentinel must still match.
			if !errors.Is(got, tc.err) {
				t.Errorf("errors.Is(classify(%s, cause), %s) = false, want true", tc.name, tc.name)
			}
			//: Code, which is what errs.HasCode and the prefix matcher read.
			if code, _ := errs.CodeOf(got); code != tc.err.Code() {
				t.Errorf("classify(%s).Code = %s, want %s", tc.name, code, tc.err.Code())
			}
			//: Reason, which is half of what errors.Is compares.
			if reason, _ := errs.ReasonOf(got); reason != tc.err.Reason() {
				t.Errorf("classify(%s).Reason = %q, want %q", tc.name, reason, tc.err.Reason())
			}
			//: The two message halves, verbatim.
			if errs.PublicOf(got) != tc.err.Public() {
				t.Errorf("classify(%s).Public = %q, want %q", tc.name, errs.PublicOf(got), tc.err.Public())
			}
			if errs.PrivateOf(got) != tc.err.Private() {
				t.Errorf("classify(%s).Private = %q, want %q", tc.name, errs.PrivateOf(got), tc.err.Private())
			}
			//: And the exit status, which WrapParams has no "inherit"
			//: spelling for and would otherwise silently fall back to 70.
			if errs.ExitCodeOf(got) != tc.err.ExitCode() {
				t.Errorf("classify(%s).ExitCode = %d, want %d", tc.name, errs.ExitCodeOf(got), tc.err.ExitCode())
			}
			//: The cause survives, or errors.Is against an os error stops
			//: working and the operating system's words are gone.
			if !errors.Is(got, cause) {
				t.Errorf("classify(%s) lost its cause", tc.name)
			}
		})
	}
}

// TestClassifyIsTotalOnANilSentinel pins the reason classify reads the five
// values through errs' accessor FUNCTIONS rather than the methods of the same
// name on its parameter.
//
// Only a bug in this package can hand it a nil sentinel, and the worst place to
// take a process down is inside the path that is already reporting a failure.
//
// Written first against the accessor functions alone, on the assumption they
// were total. They are not — a typed-nil *errs.Error is a non-nil error
// interface, so errs.CodeOf finds it in the chain and dereferences it exactly
// as sentinel.Code() would, and this test segfaulted at accessors.go:148. The
// explicit guard is what makes the claim true; reverting it restores the
// panic here.
func TestClassifyIsTotalOnANilSentinel(t *testing.T) {
	t.Parallel()

	cause := errors.New("the medium said no")
	got := classify(nil, cause)

	//: No panic, and something usable came back.
	if got == nil {
		t.Fatal("classify(nil, cause) = nil, want a typed error")
	}
	//: errs answers a zero Code with its own INVALID_WRAP_PARAMS sentinel.
	if reason, _ := errs.ReasonOf(got); reason != "INVALID_WRAP_PARAMS" {
		t.Errorf("classify(nil, cause) reason = %q, want INVALID_WRAP_PARAMS", reason)
	}
	//: And the failure it was actually told about is not thrown away.
	if !errors.Is(got, cause) {
		t.Errorf("classify(nil, cause) = %v, want it to keep the cause", got)
	}
}

// TestClassifyLetsTheOriginWin pins SDK rule 6 at this package's own call
// sites: when the cause is already one of ours, the sentinel named at the
// wrapping site must NOT relabel it.
//
// It is what lets every call site name its most specific classification
// without first asking whether the cause has one — and it is also the reason
// decodeJSONBody's DownloadFailed and APIBodyTooLarge refusals survive a
// caller that wraps them as ReleaseMetadataUnreadable.
func TestClassifyLetsTheOriginWin(t *testing.T) {
	t.Parallel()

	origin := refuse(coreupd.APIBodyTooLarge, errs.Int64("cap_bytes", 8))
	wrapped := classify(ReleaseMetadataUnreadable, origin, errs.String("query", "releases"))

	//: The origin keeps the identity a caller routes on...
	if !errors.Is(wrapped, coreupd.APIBodyTooLarge) {
		t.Errorf("errors.Is(wrapped, APIBodyTooLarge) = false, want true")
	}
	//: ...and the relabelling did NOT take.
	if errs.HasCode(wrapped, CodeReleaseMetadataUnreadable) && !errs.HasCode(origin, CodeReleaseMetadataUnreadable) {
		//: A trail entry is fine; the ORIGIN code changing is not.
		if code, _ := errs.CodeOf(wrapped); code != coreupd.CodeAPIBodyTooLarge {
			t.Errorf("wrapped origin code = %s, want %s", code, coreupd.CodeAPIBodyTooLarge)
		}
	}
	//: Both wrap sites' fields reach the diagnostic half.
	detail := diagnose(wrapped)
	for _, want := range []string{"cap_bytes=8", "query=releases"} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnose(wrapped) = %q, want %q", detail, want)
		}
	}
}

// TestDiagnoseSaysNothingTwice pins foreignCause's one judgement: a refusal
// this package DECIDED has no foreign cause to quote, and quoting its own
// sentinel back would print the sentence the reader has just read.
func TestDiagnoseSaysNothingTwice(t *testing.T) {
	t.Parallel()

	decided := refuse(coreupd.DraftRelease, errs.String("tag", "v2.0.0"))
	//: fields yes, a repeat of the sentence no.
	if got := diagnose(decided); got != "tag=v2.0.0" {
		t.Errorf("diagnose(refuse(...)) = %q, want exactly %q", got, "tag=v2.0.0")
	}

	reported := classify(StagingFailed, errors.New("no space left on device"), errs.String("step", "write_temp"))
	//: and here the operating system's words are the whole point.
	if got := diagnose(reported); !strings.Contains(got, "cause=no space left on device") {
		t.Errorf("diagnose(classify(...)) = %q, want it to quote the cause", got)
	}
}

// TestElevationFailureKeepsTheProcessResult pins that the one composed error
// this package builds still says what the escalation actually did.
//
// finalizeReplacement joins the rename's os.ErrPermission with the elevation's
// own error, and diagnose used to stop at the join — rendering "permission
// denied" followed by our own ELEVATION_FAILED sentence, the sentence the
// reader had just been shown, and never the process result underneath it.
// When `sudo -n mv` fails with no output, that result is the only thing there
// is: sudo_output is empty and "exit status 1" is the whole diagnosis.
func TestElevationFailureKeepsTheProcessResult(t *testing.T) {
	t.Parallel()

	//: sudo exiting non-zero while printing NOTHING is the hard case.
	silent := classify(ElevationFailed, errors.New("exit status 1"),
		errs.String("sudo_output", ""))
	joined := classify(ReplacementFailed, errors.Join(os.ErrPermission, silent),
		errs.String("step", "rename"), errs.Bool("elevated", true))

	detail := diagnose(joined)
	//: both halves of what happened, and in the order they happened.
	for _, want := range []string{"permission denied", "exit status 1", "elevated=true"} {
		//: each is a fact no other line carries.
		if !strings.Contains(detail, want) {
			t.Errorf("diagnose(joined) = %q, want %q", detail, want)
		}
	}
	//: and NOT our own public sentence quoted back inside the cause.
	if strings.Contains(detail, "the elevated replacement was authorised") {
		t.Errorf("diagnose(joined) = %q, want it not to repeat the public sentence", detail)
	}
	//: neither classification is lost to a caller that routes on them.
	if !errors.Is(joined, os.ErrPermission) || !errors.Is(joined, ElevationFailed) {
		t.Errorf("joined = %v, want both os.ErrPermission and ElevationFailed", joined)
	}
}

// TestExplainUpgradeFailurePrintsTheDiagnosticHalf pins the consequence of the
// split for the one reader entitled to all of it.
//
// Before the conversion the cause's own words were concatenated into the
// message, so `%v` printed them. Afterwards err.Error() is wire-safe and does
// not — so an operator told "the update could not be staged" and nothing else
// would have lost "no space left on device", which is the only actionable
// half. ExplainUpgradeFailure prints it instead.
func TestExplainUpgradeFailurePrintsTheDiagnosticHalf(t *testing.T) {
	t.Parallel()

	err := classify(StagingFailed, errors.New("no space left on device"),
		errs.String("step", "create_temp"),
		errs.String("dir", "/usr/local/bin"))

	var out strings.Builder
	testSource.ExplainUpgradeFailure(&out, "upgrade failed", err)
	text := out.String()

	for _, want := range []string{
		"upgrade failed",
		"the update could not be staged",
		"step=create_temp",
		"dir=/usr/local/bin",
		"no space left on device",
	} {
		//: every one of the five is something an operator acts on.
		if !strings.Contains(text, want) {
			t.Errorf("ExplainUpgradeFailure() = %q, want it to contain %q", text, want)
		}
	}
}

// TestUnexpectedStatusCarriesTheStatus pins that the status code an operator
// needs survives the move out of the message, on every path that raises it.
//
// It is the one particular that is NOT sensitive and was still removed from
// the sentence, because "the release host answered unexpectedly: 404" and
// "...: 500" are two different remedies and neither belongs in a response
// body this SDK does not own.
func TestUnexpectedStatusCarriesTheStatus(t *testing.T) {
	t.Parallel()

	svc := NewUpdaterWithDeps("v1.0.0", testSource,
		sizedFetcher{body: "nope", status: http.StatusTeapot}, nil, nil)

	_, err := svc.getLatestVersion()
	//: The classification a caller routes on.
	if !errors.Is(err, coreupd.UnexpectedStatus) {
		t.Fatalf("getLatestVersion() error = %v, want UnexpectedStatus", err)
	}
	//: The number an operator needs, in the half allowed to carry it.
	if got := diagnose(err); !strings.Contains(got, "status=418") {
		t.Errorf("diagnose(err) = %q, want it to name the status", got)
	}
	//: And not in the half that may cross a wire.
	if strings.Contains(err.Error(), "418") {
		t.Errorf("err.Error() = %q, want the status kept out of the public half", err.Error())
	}
}
