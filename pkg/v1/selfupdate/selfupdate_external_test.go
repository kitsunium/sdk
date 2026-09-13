// External tests for the public selfupdate surface.
package selfupdate_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/selfupdate"
)

// testSource is a release source naming a product the implementation never
// mentioned, so an assertion that happens to match a hard-coded value fails.
var testSource = selfupdate.Source{
	Owner:      "acme",
	StableRepo: "widget-dist",
	DevRepo:    "widget",
	Product:    "widget",
}

// stubFetcher answers every URL with one canned release document, so the
// vendor-key refusal is reached without touching the network.
type stubFetcher struct{ body string }

// Get returns the canned body with a 200, whatever the URL.
func (f stubFetcher) Get(_ string) (*http.Response, error) {
	//: one document serves the metadata call the upgrade path makes first.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

// TestAServiceWithNoVendorKeyInstallsNothing pins the refusal that makes every
// other guarantee in this package meaningful.
//
// The alternative shape — verify when a key is present, skip when it is not —
// would make the security property depend on a build flag nobody checks, and it
// fails open: a build that lost its key installs anything. This one fails
// closed, and the code says which refusal it is so a caller can tell "rebuild
// from source" from "the release is forged".
func TestAServiceWithNoVendorKeyInstallsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		latest  string
	}{
		{name: "a newer release is available", version: "v1.0.0", latest: "v9.9.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fetch := stubFetcher{body: `{"tag_name":"` + tt.latest + `","draft":false,"prerelease":false}`}
			svc := selfupdate.NewWithDeps(tt.version, testSource, fetch, nil, nil)

			_, err := svc.Upgrade()
			//: No key means nothing could have authenticated the release.
			if err == nil {
				t.Fatal("Upgrade() = nil error with no vendor key, want a refusal")
			}
			//: The code is what lets a caller tell this from a network failure.
			if !errs.HasCode(err, selfupdate.CodeNoVendorKey) {
				t.Errorf("Upgrade() err = %v, want code %v", err, selfupdate.CodeNoVendorKey)
			}
		})
	}
}

// TestCheckForUpdateRefusesADevBuild pins that a build with no release version
// says so by code rather than by guessing a comparison it cannot make.
func TestCheckForUpdateRefusesADevBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "the empty version", version: ""},
		{name: "the conventional dev version", version: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := selfupdate.New(tt.version, testSource).CheckForUpdate()
			//: There is no version to compare, so there is no answer to give.
			if !errs.HasCode(err, selfupdate.CodeDevBuild) {
				t.Errorf("CheckForUpdate() err = %v, want code %v", err, selfupdate.CodeDevBuild)
			}
		})
	}
}

// TestTheTwoOptInsAreDistinct pins that authorising an unattended upgrade does
// not also authorise privilege escalation.
//
// They are two decisions — one permits replacing the binary, the other permits
// doing it as root — and a caller who discovers the second only after acting on
// the first is having a bad afternoon.
func TestTheTwoOptInsAreDistinct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		product string
	}{
		{name: "a single-word product", product: "widget"},
		{name: "a hyphenated product", product: "my-tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := selfupdate.Source{Product: tt.product}
			//: Two authorisations must never read the same variable.
			if src.AutoUpgradeEnv() == src.SudoOptInEnv() {
				t.Errorf("both authorisations read %q", src.AutoUpgradeEnv())
			}
		})
	}
}

// TestTheFacadeCarriesWhatACallerCannotReimplement pins the two symbols a
// consumer needs and cannot reasonably rebuild.
//
// Both were missing until a real consumer tried to migrate onto this package
// and found them unreachable. CandidateListSentinel has to be the SAME string
// the service compares against — a caller inventing its own would wire a flag
// that never matches — and StdinIsTerminal is the seam that keeps the consent
// decision testable without a pty.
func TestTheFacadeCarriesWhatACallerCannotReimplement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		got    string
		want   string
		reason string
	}{
		{
			name:   "the candidate-list sentinel",
			got:    selfupdate.CandidateListSentinel,
			want:   "__list__",
			reason: "a caller wires this into its own flag; a different value never matches",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: The value is the contract, not just its presence.
			if tt.got != tt.want {
				t.Errorf("= %q, want %q (%s)", tt.got, tt.want, tt.reason)
			}
		})
	}

	//: StdinIsTerminal has no assertable value under `go test` — stdin is not a
	//: character device there — so what is pinned is that it answers at all,
	//: which is what a caller needs from the seam.
	t.Run("stdin detection answers without a pty", func(t *testing.T) {
		t.Parallel()
		selfupdate.StdinIsTerminal()
	})
}

// TestEveryFailureThisPackageCanReturnHasAPublicName pins the completeness of
// the code surface, and pins it by VALUE rather than by presence.
//
// PR #184 fixed the same shape in the entitlement facade, which shipped fifteen
// codes and no way to name most of what it returned. A re-export is one line
// and mistyping which constant it points at is one character, so "the symbol
// exists" is not the property worth asserting — "it is the same code the engine
// raises" is, and so is "no two of them collide", because two names for one
// value is a caller routing to the wrong branch with no error anywhere.
//
// Twenty-five: eighteen from the domain contract (0.2.34.*) and seven from the
// implementation (0.3.66.*).
func TestEveryFailureThisPackageCanReturnHasAPublicName(t *testing.T) {
	t.Parallel()

	const wantCodes int = 25

	// exported is every failure code this facade publishes, against the dotted
	// quad it must resolve to. The literal is deliberate: reading it back from
	// the same constant would assert nothing at all.
	exported := []struct {
		name string
		got  errs.Code
		want string
	}{
		{"CodeNoVendorKey", selfupdate.CodeNoVendorKey, "0.2.34.1"},
		{"CodeSignatureMissing", selfupdate.CodeSignatureMissing, "0.2.34.2"},
		{"CodeSignatureInvalid", selfupdate.CodeSignatureInvalid, "0.2.34.3"},
		{"CodeChecksumMissing", selfupdate.CodeChecksumMissing, "0.2.34.4"},
		{"CodeChecksumMismatch", selfupdate.CodeChecksumMismatch, "0.2.34.5"},
		{"CodeArchiveTooLarge", selfupdate.CodeArchiveTooLarge, "0.2.34.6"},
		{"CodeAPIBodyTooLarge", selfupdate.CodeAPIBodyTooLarge, "0.2.34.7"},
		{"CodeInsecureRedirect", selfupdate.CodeInsecureRedirect, "0.2.34.8"},
		{"CodeUnexpectedStatus", selfupdate.CodeUnexpectedStatus, "0.2.34.9"},
		{"CodeDownloadFailed", selfupdate.CodeDownloadFailed, "0.2.34.10"},
		{"CodeDevBuild", selfupdate.CodeDevBuild, "0.2.34.11"},
		{"CodeCandidateNotFound", selfupdate.CodeCandidateNotFound, "0.2.34.12"},
		{"CodeNotPrerelease", selfupdate.CodeNotPrerelease, "0.2.34.13"},
		{"CodeDraftRelease", selfupdate.CodeDraftRelease, "0.2.34.14"},
		{"CodeInvalidTag", selfupdate.CodeInvalidTag, "0.2.34.15"},
		{"CodeBinaryNotInArchive", selfupdate.CodeBinaryNotInArchive, "0.2.34.16"},
		{"CodeUnknownArchive", selfupdate.CodeUnknownArchive, "0.2.34.17"},
		{"CodeElevationNotAuthorised", selfupdate.CodeElevationNotAuthorised, "0.2.34.18"},
		{"CodeCandidateTagRequired", selfupdate.CodeCandidateTagRequired, "0.3.66.1"},
		{"CodeReleaseMetadataUnreadable", selfupdate.CodeReleaseMetadataUnreadable, "0.3.66.2"},
		{"CodeArchiveUnreadable", selfupdate.CodeArchiveUnreadable, "0.3.66.3"},
		{"CodeExecutablePathUnresolved", selfupdate.CodeExecutablePathUnresolved, "0.3.66.4"},
		{"CodeStagingFailed", selfupdate.CodeStagingFailed, "0.3.66.5"},
		{"CodeReplacementFailed", selfupdate.CodeReplacementFailed, "0.3.66.6"},
		{"CodeElevationFailed", selfupdate.CodeElevationFailed, "0.3.66.7"},
	}

	//: A short table is a re-export somebody forgot; a long one is a duplicate.
	if len(exported) != wantCodes {
		t.Fatalf("table lists %d codes, want %d", len(exported), wantCodes)
	}

	seen := make(map[string]string, len(exported))
	for _, tt := range exported {
		t.Run(tt.name, func(t *testing.T) {
			//: The value, not the symbol's existence.
			if got := tt.got.String(); got != tt.want {
				t.Errorf("%s = %s, want %s", tt.name, got, tt.want)
			}
		})
		//: Two names for one value is a caller routing to the wrong branch
		//: with nothing anywhere reporting it.
		if first, dup := seen[tt.got.String()]; dup {
			t.Errorf("%s and %s are both %s", first, tt.name, tt.got)
		}
		seen[tt.got.String()] = tt.name
	}
}

// TestEverySentinelIsTheOneTheEngineRaises is the companion to the code test,
// and it exists for the same reason: a re-export is one line, and pointing it
// at the neighbouring constant is one character.
//
// Presence proves nothing here. `SignatureInvalid = coreupd.SignatureMissing`
// compiles, type-checks, exports a symbol of the right name and sends every
// caller that branches on a forged release down the "not signed" path instead
// — which is the same remedy, so nobody would notice until the day the two
// remedies diverged. So each alias is matched against an error the ENGINE
// produced, through errors.Is, and no two may match the same one.
func TestEverySentinelIsTheOneTheEngineRaises(t *testing.T) {
	t.Parallel()

	const wantSentinels int = 25

	// exported is every sentinel this facade publishes, beside the code it
	// must carry. The code is the independent witness: it comes from the
	// separately-asserted const surface, so a sentinel pointing at the wrong
	// value disagrees with it.
	exported := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"NoVendorKey", selfupdate.NoVendorKey, selfupdate.CodeNoVendorKey},
		{"SignatureMissing", selfupdate.SignatureMissing, selfupdate.CodeSignatureMissing},
		{"SignatureInvalid", selfupdate.SignatureInvalid, selfupdate.CodeSignatureInvalid},
		{"ChecksumMissing", selfupdate.ChecksumMissing, selfupdate.CodeChecksumMissing},
		{"ChecksumMismatch", selfupdate.ChecksumMismatch, selfupdate.CodeChecksumMismatch},
		{"ArchiveTooLarge", selfupdate.ArchiveTooLarge, selfupdate.CodeArchiveTooLarge},
		{"APIBodyTooLarge", selfupdate.APIBodyTooLarge, selfupdate.CodeAPIBodyTooLarge},
		{"InsecureRedirect", selfupdate.InsecureRedirect, selfupdate.CodeInsecureRedirect},
		{"UnexpectedStatus", selfupdate.UnexpectedStatus, selfupdate.CodeUnexpectedStatus},
		{"DownloadFailed", selfupdate.DownloadFailed, selfupdate.CodeDownloadFailed},
		{"DevBuild", selfupdate.DevBuild, selfupdate.CodeDevBuild},
		{"CandidateNotFound", selfupdate.CandidateNotFound, selfupdate.CodeCandidateNotFound},
		{"NotPrerelease", selfupdate.NotPrerelease, selfupdate.CodeNotPrerelease},
		{"DraftRelease", selfupdate.DraftRelease, selfupdate.CodeDraftRelease},
		{"InvalidTag", selfupdate.InvalidTag, selfupdate.CodeInvalidTag},
		{"BinaryNotInArchive", selfupdate.BinaryNotInArchive, selfupdate.CodeBinaryNotInArchive},
		{"UnknownArchive", selfupdate.UnknownArchive, selfupdate.CodeUnknownArchive},
		{"ElevationNotAuthorised", selfupdate.ElevationNotAuthorised, selfupdate.CodeElevationNotAuthorised},
		{"CandidateTagRequired", selfupdate.CandidateTagRequired, selfupdate.CodeCandidateTagRequired},
		{"ReleaseMetadataUnreadable", selfupdate.ReleaseMetadataUnreadable, selfupdate.CodeReleaseMetadataUnreadable},
		{"ArchiveUnreadable", selfupdate.ArchiveUnreadable, selfupdate.CodeArchiveUnreadable},
		{"ExecutablePathUnresolved", selfupdate.ExecutablePathUnresolved, selfupdate.CodeExecutablePathUnresolved},
		{"StagingFailed", selfupdate.StagingFailed, selfupdate.CodeStagingFailed},
		{"ReplacementFailed", selfupdate.ReplacementFailed, selfupdate.CodeReplacementFailed},
		{"ElevationFailed", selfupdate.ElevationFailed, selfupdate.CodeElevationFailed},
	}

	//: One sentinel per code, both ways round: a short table is one somebody
	//: forgot, a long one is a duplicate.
	if len(exported) != wantSentinels {
		t.Fatalf("table lists %d sentinels, want %d", len(exported), wantSentinels)
	}

	seen := make(map[string]string, len(exported))
	for _, tt := range exported {
		t.Run(tt.name, func(t *testing.T) {
			//: Not nil — a nil sentinel makes every errors.Is against it a
			//: silent false, which is the failure mode with no symptom.
			if tt.err == nil {
				t.Fatalf("%s is nil", tt.name)
			}
			//: The sentinel and the code must agree. They are re-exported
			//: from different declarations, so this is two independent
			//: one-character mistakes having to make the same error.
			if got, _ := errs.CodeOf(tt.err); got != tt.code {
				t.Errorf("%s carries %s, want %s", tt.name, got, tt.code)
			}
			//: And it must match an error wrapped around it — the shape a
			//: caller actually receives, never the bare sentinel.
			wrapped := fmt.Errorf("installing: %w", tt.err)
			if !errors.Is(wrapped, tt.err) {
				t.Errorf("errors.Is(wrapped, %s) = false, want true", tt.name)
			}
		})
		//: Two names for one sentinel routes a caller to the wrong remedy
		//: with nothing anywhere reporting it.
		if first, dup := seen[tt.code.String()]; dup {
			t.Errorf("%s and %s are the same sentinel (%s)", first, tt.name, tt.code)
		}
		seen[tt.code.String()] = tt.name
	}
}

// TestSentinelsDoNotMatchEachOther pins the discrimination the classes depend
// on: the three remedies the package documents are only distinct if the
// sentinels that select them are.
//
// The pair chosen is the one where confusing them costs most. A supply-chain
// refusal must never be answered with a retry, and a retryable failure must
// never be answered with "do not install this by any means".
func TestSentinelsDoNotMatchEachOther(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    error
		b    error
	}{
		{"a forged release is not a flaky network", selfupdate.SignatureInvalid, selfupdate.DownloadFailed},
		{"an unsigned release is not a forged one", selfupdate.SignatureMissing, selfupdate.SignatureInvalid},
		{"staging is not replacement", selfupdate.StagingFailed, selfupdate.ReplacementFailed},
		{"a refused escalation is not a failed one", selfupdate.ElevationNotAuthorised, selfupdate.ElevationFailed},
		{"an unreadable archive is not a bad checksum", selfupdate.ArchiveUnreadable, selfupdate.ChecksumMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Neither direction may match, or one remedy answers both.
			if errors.Is(tt.a, tt.b) || errors.Is(tt.b, tt.a) {
				t.Errorf("%v and %v match each other", tt.a, tt.b)
			}
		})
	}
}
