// Package selfupdate — consent: whether a caller that did not ask to upgrade
// may nonetheless replace this binary, and the advice printed when it may not.
// Package updater — CONSENT for an upgrade nobody typed.
//
// An explicit `upgrade` command is a deliberate act and needs no permission: a
// it IS the permission. The licence gate is the opposite case. It fires from
// the root command's PersistentPreRun on every invocation, so a plain
// routine command that could reach a code path downloading an archive,
// chmods it 0755, moves it over the running executable and, where sudoers
// allows it, does that last step as root — none of which the person who
// typed `run` asked for. The gate's own comment already conceded the point:
// "this is the one moment a user is staring at a pause they did not ask for".
//
// A mandatory update that simply refuses is useless, so refusing is not what
// this does. It asks when someone is there to answer, takes an explicit
// out-of-band authorisation when nobody is (CI, devcontainers, hooks), and
// when it does decline it prints every way forward rather than leaving a
// dead end.
package selfupdate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// AutoUpgradeEnv authorises an upgrade the user did not explicitly request —
// the licence gate's mandatory update. Set it to 1/true/yes/on for CI images,
// devcontainers and hook-driven runs where no one can answer a prompt.
//
// Exported because the refusal message names it, and a refusal that does not

// AuthoriseUnattendedUpgrade reports whether a caller that did not ask to
// upgrade may nonetheless replace this binary.
//
// out receives the prompt; in supplies the answer; interactive says whether
// there is a human on the other end (the caller decides — see
// StdinIsTerminal — so a test does not need a pty). The environment is read
// here rather than passed so every call site honours the same variable.
//
// The order is: an explicit authorisation decides, in EITHER direction;
// otherwise a human is asked; otherwise the answer is no.
func (s SourceValue) AuthoriseUnattendedUpgrade(out io.Writer, in io.Reader, interactive bool) bool {
	granted, decided := consentFromEnv(os.Getenv(s.AutoUpgradeEnv()))
	//: An explicit value settles it without a prompt — that is the whole
	//: point of having one, for CI and devcontainers.
	if decided {
		//: Report the operator's standing answer.
		return granted
	}

	//: Nobody can answer, so nobody consented. Silence is not permission.
	if !interactive {
		//: Refuse; the caller prints the ways forward.
		return false
	}

	//: A human is watching — ask, rather than refusing a mandatory update
	//: they would obviously have accepted.
	return askUpgradeConsent(out, in)
}

// ExplainUpgradeRefusal prints every way to get the required update
// installed after consent was declined or could not be obtained.
//
// It is deliberately exhaustive, including the sudo opt-in: the two
// authorisations are separate on purpose (one permits replacing the binary,
// the other permits doing it as root) and discovering the second one only
// after acting on the first is a bad afternoon.
func (s SourceValue) ExplainUpgradeRefusal(out io.Writer) {
	fmt.Fprintf(out, "%s does not download and replace its own binary without being asked.\n", s.Product)
	fmt.Fprintf(out, "  - run `%s upgrade` to install the required version now, or\n", s.Product)
	fmt.Fprintf(out, "  - set %s=1 to authorise updates that were not explicitly requested\n", s.AutoUpgradeEnv())
	fmt.Fprintf(out, "    (CI images, devcontainers, hook-driven runs — nothing there can answer a prompt)\n")
	fmt.Fprintf(out, "  - installing into a directory this user cannot write additionally needs %s=1\n", s.SudoOptInEnv())
}

// ExplainUpgradeFailure prints what a refused upgrade means and what, if
// anything, resolves it. `action` names the operation that failed, so the
// same advice serves the gate ("cannot apply the required update") and the
// deliberate command ("upgrade failed").
//
// It exists because exit.Code's message parameter only reaches the structured
// --debug log (pkg/exit's logEvent is a no-op unless --debug is set): without
// this, an upgrade against a forged or unsigned release printed
// "Checking for updates..." and then nothing, exiting non-zero with no reason
// given. For a network blip that is merely poor; for an authenticity refusal
// it hides the one signal that must not be missed, and invites the user to
// go and install the bad release by hand.
func (s SourceValue) ExplainUpgradeFailure(out io.Writer, action string, err error) {
	fmt.Fprintf(out, "%s: %v\n", action, err)
	//: Dispatch based on the variant to apply the correct logic.
	switch {
	//: A supply-chain signal. There is no retry that fixes this and no safe
	//: manual workaround, so say both.
	case errors.Is(err, coreupd.SignatureMissing), errors.Is(err, coreupd.SignatureInvalid):
		fmt.Fprintln(out, "  this release is not signed by the vendor key this build trusts.")
		fmt.Fprintln(out, "  do NOT install it by hand: a missing or wrong signature is exactly what a")
		fmt.Fprintln(out, "  substituted release looks like, and the checksum cannot tell the two apart.")
	//: The developer case: a local build has a compiler, not an update path.
	case errors.Is(err, coreupd.NoVendorKey):
		fmt.Fprintln(out, "  this binary was built from source and trusts no release signing key.")
		fmt.Fprintln(out, "  rebuild from source rather than self-updating.")
	//: Recoverable, but only through an opt-in the user has to know exists.
	case errors.Is(err, coreupd.ElevationNotAuthorised):
		fmt.Fprintf(out, "  set %s=1 to allow `sudo -n mv` into the install directory,\n", s.SudoOptInEnv())
		fmt.Fprintln(out, "  or re-run as the owner of that path.")
	//: Signed manifest, wrong bytes — ambiguous between corruption and
	//: tampering after signing, and the user cannot tell which. Say so.
	case errors.Is(err, coreupd.ChecksumMismatch), errors.Is(err, coreupd.ChecksumMissing):
		fmt.Fprintln(out, "  the archive does not match the signed manifest — a corrupted download,")
		fmt.Fprintln(out, "  or a release whose assets were changed after it was signed.")
	//: Everything else (network, permissions, a 500) is worth retrying.
	default:
		fmt.Fprintln(out, "  retry once the problem is resolved.")
	}
}

// consentFromEnv reads the standing authorisation.
//
// The second result distinguishes "no answer recorded" from "the answer is
// no", which the caller needs: only the first falls through to a prompt.
// Unset and empty are the same thing — an exported variable set to nothing
// is not a decision.
func consentFromEnv(value string) (granted, decided bool) {
	//: Nothing recorded: fall through to asking.
	if strings.TrimSpace(value) == "" {
		//: No standing answer.
		return false, false
	}
	//: Anything set that is not an accepted "yes" is a NO, typos included.
	//: An opt-in that reads an unrecognised value as permission is not one.
	return truthyEnv(value), true
}

// askUpgradeConsent puts the question to the human at the keyboard.
//
// Default-no: a bare Enter, EOF, a closed stdin and an unreadable one all
// decline. The only accepted answers are an explicit yes.
func askUpgradeConsent(out io.Writer, in io.Reader) bool {
	//: An absent reader cannot answer; treat it as the absent human it is.
	if in == nil {
		//: Declined.
		return false
	}
	fmt.Fprint(out, "install it now, replacing this binary? [y/N]: ")

	answer, err := bufio.NewReader(in).ReadString('\n')
	//: A read that returned nothing at all (EOF on a closed stdin) is not an
	//: answer. A read that errored WITH data is one — ReadString reports EOF
	//: alongside the final unterminated line.
	if err != nil && answer == "" {
		fmt.Fprintln(out)
		//: Declined.
		return false
	}

	//: Dispatch based on the variant to apply the correct logic.
	switch strings.ToLower(strings.TrimSpace(answer)) {
	//: The only two spellings that mean yes.
	case "y", "yes":
		//: Authorised for this invocation only.
		return true
	//: Everything else, empty line included.
	default:
		//: Declined.
		return false
	}
}

// StdinIsTerminal reports whether a human could answer a prompt on this
// process's standard input.
//
// Callers pass the result to AuthoriseUnattendedUpgrade rather than letting
// it detect its own terminal, so the decision stays testable without a pty.
func StdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	//: A stat we cannot read is not evidence of a human.
	if err != nil {
		//: Assume unattended.
		return false
	}
	//: Delegate the mode test so it is assertable without a real terminal.
	return terminalLike(info.Mode())
}

// terminalLike reports whether a file mode is that of a character device —
// what a tty is, and what a pipe, a regular file and /dev/null are not.
func terminalLike(mode os.FileMode) bool {
	//: A pipe (`echo x | tool run`) or a redirect has no one behind it.
	return mode&os.ModeCharDevice != 0
}
