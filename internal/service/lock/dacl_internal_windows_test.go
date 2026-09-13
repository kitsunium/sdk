//go:build windows

package lock

import (
	"os"
	"testing"
)

// This file is INTERNAL (package lock) because what it measures is the walk
// itself rather than the locker's public verdict. It carries the same build
// constraint as dacl_windows.go, and the lane that executes it is the
// `windows` job of .github/workflows/e2e-cross.yml.

// sidBuiltinUsers is S-1-5-32-545, BUILTIN\Users — the group every local
// interactive account belongs to, and on a domain-joined machine one that
// contains Domain Users as well.
const sidBuiltinUsers string = "S-1-5-32-545"

// TestWhetherBuiltinUsersCanPlantInDirectoriesWindowsShips turns ADR 0084
// §D2's deferral into a measurement, on the only kernel that can take it.
//
// # The question
//
// `anyoneSids` is Everyone and Authenticated Users, the pair ADR 0081 §D5
// named. BUILTIN\Users is the obvious third and is EXCLUDED, on the stated
// grounds that "Windows itself grants Users write on directories it ships, so
// adding it would refuse deployments this change has no measurement about".
//
// That is an argument, not evidence, and it was raised against this change as
// a security finding: a directory granting a planting right to BUILTIN\Users
// is one every local account can plant in, which is exactly what the rule
// claims to refuse.
//
// # What this test does about it
//
// It reads the real DACL of directories Windows installs, with BUILTIN\Users
// added to the table, and asserts the conclusion that justifies the
// exclusion: at least one of them grants that group a planting right.
//
// If that holds, including Users would refuse a directory the operating system
// itself configured, and the deferral stands on evidence rather than on a
// sentence. If it FAILS, the premise is wrong on this kernel and Users should
// be in `anyoneSids` — so a red lane here is not a flake, it is the decision
// being made for us. Either way the ADR stops resting on an assertion.
//
// It deliberately does not test %TEMP%, which is per-user and proves nothing
// either way.
func TestWhetherBuiltinUsersCanPlantInDirectoriesWindowsShips(t *testing.T) {
	//: deliberately NOT t.Parallel(). This test mutates a package variable the
	//: production walk reads, and Go resumes paused parallel tests only after
	//: every top-level test has been started — so a sequential test cannot
	//: overlap with them, and a parallel one here would race the ordinary
	//: acquisitions in dacl_windows_test.go.
	//: the two shapes a real deployment actually puts a lock directory under.
	candidates := []string{os.Getenv("ProgramData"), os.Getenv("SystemRoot") + `\Temp`}
	granted := map[string]string{}
	//: one reading per candidate; a candidate that is not on this image is
	//: skipped rather than counted either way.
	for _, dir := range candidates {
		if dir == "" {
			//: the environment variable is unset on this image.
			continue
		}
		if _, statErr := os.Stat(dir); statErr != nil {
			//: not present on this image.
			continue
		}
		//: the SAME walk production uses, with the table widened by one entry,
		//: so this measures the shipped mechanism rather than a reimplementation.
		if writable, observed := withUsers(func() (bool, string) { return dirWritableByAnyone(dir) }); writable {
			granted[dir] = observed
		}
	}
	//: at least one directory Windows ships grants BUILTIN\Users a planting
	//: right — which is what makes including it a breaking change rather than
	//: a free win.
	if len(granted) == 0 {
		t.Fatalf("no directory Windows ships was found granting BUILTIN\\Users a planting right, over %v — ADR 0084 §D2's reason for excluding S-1-5-32-545 does not hold on this kernel, and it belongs in anyoneSids", candidates)
	}
	//: the finding is recorded in the failure message of the assertion above
	//: rather than logged, because e2e-cross runs `go test` without -v and
	//: discards a passing package's output (ADR 0082 §D5).
	t.Logf("BUILTIN\\Users holds a planting right on: %v", granted)
}

// withUsers runs probe with BUILTIN\Users temporarily added to [anyoneSids].
//
// It mutates a package variable and puts it back, which is safe only because
// its one caller is a SEQUENTIAL test: Go resumes paused parallel tests after
// every top-level test has been started, so nothing else in the package is
// reading `anyoneSids` while this runs. It is confined to this one measurement
// deliberately — the alternative, parameterising the walk, would change
// production code to serve a test.
func withUsers(probe func() (bool, string)) (writable bool, observed string) {
	original := anyoneSids
	anyoneSids = append(append([]string{}, original...), sidBuiltinUsers)
	defer func() { anyoneSids = original }()
	//: the widened table, on the real walk.
	return probe()
}
