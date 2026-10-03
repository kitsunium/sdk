package semver_test

import (
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/semver"
)

// TestFacade pins every function through its public name, as a consumer —
// the framework first among them — calls it: validity and the pre-release,
// precedence including the orderings a string comparison gets wrong, a sort
// with Compare, and the three pseudo-version readings.
func TestFacade(t *testing.T) {
	t.Parallel()
	if !semver.IsValid("v1.2.3-rc.1+build.5") || semver.IsValid("1.2.3") {
		t.Error("IsValid does not read the grammar: a version refused, or a version without its v accepted")
	}
	if got := semver.Prerelease("v1.0.0-rc.1+build"); got != "-rc.1" {
		t.Errorf("Prerelease = %q, want %q", got, "-rc.1")
	}
	if semver.Compare("v1.9.12", "v1.10.0") != -1 || semver.Compare("v1.0.0-beta.11", "v1.0.0-beta.2") != 1 {
		t.Error("Compare ordered numbers as text")
	}
	if semver.Compare("v1.0.0+a", "v1.0.0+b") != 0 || semver.Compare("garbage", "v0.0.0") != -1 {
		t.Error("Compare weighed build metadata, or put an invalid string above a version")
	}
	tags := []string{"v1.10.0", "v1.2.0", "not-a-tag", "v1.10.0-rc.1", "v1.9.0"}
	slices.SortFunc(tags, semver.Compare)
	if want := []string{"not-a-tag", "v1.2.0", "v1.9.0", "v1.10.0-rc.1", "v1.10.0"}; !slices.Equal(tags, want) {
		t.Errorf("sorted with Compare = %q, want %q", tags, want)
	}
	const pseudo = "v1.2.4-0.20260924100234-23e4c32e7484+incompatible"
	if !semver.IsPseudoVersion(pseudo) || semver.IsPseudoVersion("v1.2.4") {
		t.Error("IsPseudoVersion does not tell a pseudo-version from a release")
	}
	if rev, ok := semver.PseudoVersionRev(pseudo); rev != "23e4c32e7484" || !ok {
		t.Errorf("PseudoVersionRev = %q, %v; want %q, true", rev, ok, "23e4c32e7484")
	}
	want := time.Date(2026, 9, 24, 10, 2, 34, 0, time.UTC)
	if at, ok := semver.PseudoVersionTime(pseudo); !at.Equal(want) || !ok {
		t.Errorf("PseudoVersionTime = %v, %v; want %v, true", at, ok, want)
	}
	if _, ok := semver.PseudoVersionTime("v0.0.0-20261399000000-abcdefabcdef"); ok {
		t.Error("PseudoVersionTime read a thirteenth month")
	}
}
