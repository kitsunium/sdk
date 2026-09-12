// Package selfupdate — privilege escalation: the second, separate opt-in that a
// replacement into a directory this user cannot write requires.
// Package updater — privilege escalation, and the explicit consent it now
// requires.
//
// finalizeReplacement falls back to `sudo -n mv` when the plain rename over
// the running executable is refused for permissions. That fallback used to
// fire unconditionally, which meant an ordinary gated command — a
// command nobody asked to install anything — could end up executing a
// privileged move of a file it had just downloaded. Where sudoers grants
// NOPASSWD (devcontainers, CI images, plenty of laptops) that completes
// silently and writes an attacker-chosen file into a root-owned directory.
//
// Escalation is now opt-in. Not asking is the default, and the refusal names
// the two ways forward so it is never a dead end.
package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// truthyEnv reports whether an environment variable's value means "yes".
//
// The accepted set is closed and case-insensitive. Anything else — including
// a typo, and including "0"/"false" — is NOT consent: an opt-in that treats
// an unrecognised value as permission is not an opt-in.
func truthyEnv(value string) bool {
	//: Dispatch based on the variant to apply the correct logic.
	switch strings.ToLower(strings.TrimSpace(value)) {
	//: The spellings a human or a CI file would reasonably write.
	case "1", "true", "yes", "y", "on":
		//: Authorised.
		return true
	//: Everything else, unset included.
	default:
		//: Not authorised.
		return false
	}
}

// guardedSudoMove is the Service.elevate default: it escalates only when
// SudoOptInEnv authorises it, and otherwise refuses without running anything.
//
// The environment is read here rather than captured at construction so an
// unattended caller can set the variable for exactly the invocation that
// needs it, and so a long-lived process cannot carry an authorisation from a
// moment when the operator meant something else.
func (s SourceValue) guardedSudoMove(tmpPath, execPath string) error {
	//: No authorisation means no `sudo` process is created at all — this
	//: returns before exec.Command, which is the property the test pins.
	if !truthyEnv(os.Getenv(s.SudoOptInEnv())) {
		//: Name both ways forward; a bare refusal here strands the user.
		return fmt.Errorf(
			"%w: replacing %s needs elevated rights — re-run as the owner of that path, or set %s=1 to allow `sudo -n mv`",
			coreupd.ElevationNotAuthorised, execPath, s.SudoOptInEnv())
	}

	//: Authorised — perform the escalated move.
	return sudoMove(tmpPath, execPath)
}

// sudoMove replaces execPath with tmpPath via a non-interactive `sudo -n
// mv` — the escalation finalizeReplacement falls back to when the plain
// rename is refused for permissions, and the same one the devcontainer
// installer's conditional (`ktn.md`) uses for a root-owned target
// directory. `-n` disables the password prompt entirely: without a cached
// credential or a NOPASSWD sudoers rule it fails immediately instead of
// hanging an unattended upgrade.
//
// It is unexported and reached only through guardedSudoMove, so there is no
// call path that escalates without the opt-in.
func sudoMove(tmpPath, execPath string) error {
	//: exec.Command rather than exec.CommandContext: `-n` already guarantees
	//: sudo returns immediately rather than waiting on a prompt, and
	//: cancelling a `mv` halfway is exactly the outcome to avoid on the binary
	//: being replaced.
	out, err := exec.Command("sudo", "-n", "mv", tmpPath, execPath).CombinedOutput()
	//: Surface sudo's own stderr (e.g. "sudo: a password is required") — it
	//: names the real blocker far better than the wrapping exec.ExitError.
	if err != nil {
		//: Wrap sudo's own message so the caller reports the real blocker.
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	//: The binary was replaced in place.
	return nil
}
