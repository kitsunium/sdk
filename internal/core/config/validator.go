// Package config — the optional self-validation contract.
package config

// Validator is implemented by a decoded config struct that wants to self-check
// after Load. A non-nil return aborts the Load with CONFIG_VALIDATION_FAILED.
type Validator interface {
	// Validate reports whether the decoded configuration is internally consistent.
	Validate() error
}
