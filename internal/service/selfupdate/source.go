// Package selfupdate — the release source: which project's releases this binary
// updates itself from, and what its artefacts are called.
package selfupdate

import "strings"

// defaultProductEnvPrefix is the environment-variable prefix used when a
// SourceValue names none. It is deliberately generic: a build that forgets to
// set Product still gets a working, if unbranded, opt-in variable rather than
// silently sharing one with every other SDK consumer on the machine.
const defaultProductEnvPrefix string = "SELFUPDATE"

// envSuffixAutoUpgrade is appended to the product prefix for the unattended
// upgrade opt-in.
const envSuffixAutoUpgrade string = "_AUTO_UPGRADE"

// envSuffixAllowSudo is appended to the product prefix for the privilege
// escalation opt-in.
const envSuffixAllowSudo string = "_ALLOW_SUDO"

// SourceValue says where releases come from and what they are called.
//
// It is the whole of what the source implementation hard-coded. Every field is a
// property of one product's distribution, not of the update mechanism, so
// keeping them as constants is what made that implementation un-reusable.
//
// StableRepo and DevRepo can differ, and the reason is worth stating: a project
// whose source repository goes private still has to serve already-installed
// binaries their update path, which means a public mirror for stable releases
// while candidates stay in the source repository behind credentials the public
// binary does not carry. When DevRepo is empty, StableRepo serves both.
type SourceValue struct {
	// Owner is the release host account, e.g. "kodflow".
	Owner string
	// StableRepo is the repository serving stable releases.
	StableRepo string
	// DevRepo is the repository serving release candidates. Empty means
	// candidates come from StableRepo too.
	DevRepo string
	// Product is the binary's name. It is both the archive-asset stem
	// ("<product>_<goos>_<goarch>.tar.gz") and, uppercased, the environment
	// variable prefix for the two opt-ins.
	Product string
}

// candidateRepo returns the repository that serves release candidates: DevRepo
// when set, StableRepo otherwise.
func (s SourceValue) candidateRepo() string {
	//: an empty DevRepo means one repository serves both channels.
	if s.DevRepo == "" {
		//: fall back to the stable repository.
		return s.StableRepo
	}

	//: the dedicated candidate repository.
	return s.DevRepo
}

// envPrefix returns the environment-variable prefix for this product, uppercased
// with non-alphanumerics folded to underscore so a product name like
// "my-tool" yields MY_TOOL_AUTO_UPGRADE rather than an unreachable variable.
func (s SourceValue) envPrefix() string {
	//: an unnamed product gets the generic prefix rather than a bare "_".
	if s.Product == "" {
		//: the documented fallback.
		return defaultProductEnvPrefix
	}
	var b strings.Builder
	//: fold every byte that cannot appear in a portable env name.
	for _, r := range strings.ToUpper(s.Product) {
		//: letters and digits pass through; everything else becomes "_".
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}

	//: the normalised prefix.
	return b.String()
}

// AutoUpgradeEnv is the variable authorising an upgrade the user did not ask
// for, e.g. MY_TOOL_AUTO_UPGRADE.
func (s SourceValue) AutoUpgradeEnv() string {
	//: prefix plus the fixed suffix.
	return s.envPrefix() + envSuffixAutoUpgrade
}

// SudoOptInEnv is the variable authorising privilege escalation when the install
// directory is not writable, e.g. MY_TOOL_ALLOW_SUDO.
func (s SourceValue) SudoOptInEnv() string {
	//: prefix plus the fixed suffix.
	return s.envPrefix() + envSuffixAllowSudo
}
