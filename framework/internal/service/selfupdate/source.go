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

// autoUpgradeEnv is SourceValue.AutoUpgradeEnv's body: decl_gen.go writes SourceValue.AutoUpgradeEnv, from the
// design, as one call of it.
func (s SourceValue) autoUpgradeEnv() string {
	//: prefix plus the fixed suffix.
	return s.envPrefix() + envSuffixAutoUpgrade
}

// sudoOptInEnv is SourceValue.SudoOptInEnv's body: decl_gen.go writes SourceValue.SudoOptInEnv, from the
// design, as one call of it.
func (s SourceValue) sudoOptInEnv() string {
	//: prefix plus the fixed suffix.
	return s.envPrefix() + envSuffixAllowSudo
}
