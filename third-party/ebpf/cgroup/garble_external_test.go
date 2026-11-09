package cgroup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// garbleEnv names the garble binary the check builds with. The check is
// opt-in: a garble on PATH may have been built by another Go, which garble
// refuses, so only a lane that installed it for this Go names it.
const garbleEnv = "KITSUNIUM_GARBLE"

// TestGarbleTinyBuildLinksTheBackend builds testdata/smoke — a program that
// imports this package blank and probes — with garble -tiny, then runs it:
// the obfuscated, stripped binary still registers the backend at import
// and still answers with typed codes. It runs when KITSUNIUM_GARBLE names
// a garble built by the Go running the test; CI's garble-tiny job installs
// the pinned garble and runs it by name (bazel-ci.yml, rule 12).
func TestGarbleTinyBuildLinksTheBackend(t *testing.T) {
	garble := os.Getenv(garbleEnv)
	//: opt-in: the garble-tiny job names its garble.
	if garble == "" {
		t.Skip("KITSUNIUM_GARBLE is unset; CI's garble-tiny job runs this test")
	}
	bin := filepath.Join(t.TempDir(), "smoke")
	build := exec.Command(garble, "-tiny", "build", "-o", bin, "./testdata/smoke")
	//: the build.
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("garble -tiny build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).CombinedOutput()
	//: the backend answered, typed.
	if err != nil || !strings.HasPrefix(string(out), "linked: ") {
		t.Fatalf("smoke: %v\n%s", err, out)
	}
	t.Logf("garble -tiny smoke: %s", strings.TrimSpace(string(out)))
}
