package self_test

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// TestReadIdentity pins #938: the digest is the SHA-256 of the file
// os.Executable names, computed by the test itself; on Linux the start time
// is the kernel's and within 2 s of the package's own start, and the build id
// is what `go tool buildid` prints when the go command is at hand.
func TestReadIdentity(t *testing.T) {
	t.Parallel()
	identity := self.ReadIdentity()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Skipf("the executable is unreadable: %v", err)
	}
	if !identity.DigestOK || identity.Digest != sha256.Sum256(body) || identity.Executable != exe {
		t.Fatalf("Digest of %s = %x (ok %v), want %x", identity.Executable, identity.Digest, identity.DigestOK, sha256.Sum256(body))
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		started := self.ReadStats().Started
		gap := identity.StartedAt.Sub(started)
		if identity.StartedAtSource != self.StartSourceKernel || gap > 2*time.Second || gap < -2*time.Second {
			t.Fatalf("StartedAt = %v (%v), the package started at %v", identity.StartedAt, identity.StartedAtSource, started)
		}
	}
	goTool, lookErr := exec.LookPath("go")
	if runtime.GOOS != "linux" || lookErr != nil {
		return
	}
	out, err := exec.Command(goTool, "tool", "buildid", exe).Output()
	if err != nil {
		t.Skipf("go tool buildid: %v", err)
	}
	if want := strings.TrimSpace(string(out)); identity.BuildID != want {
		t.Fatalf("BuildID = %q, go tool buildid says %q", identity.BuildID, want)
	}
}
