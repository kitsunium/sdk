// Package config declares the configuration port of the SDK: a Source that
// yields a flat key→value map, a Validator the decoded struct may implement, and
// a Watcher that re-fires on change. A core sibling admitted by ADR 0028 (closes
// the Phase-B wave). Concrete sources (env, file), the merge+decode loader, and
// the cross-OS poll watcher live in internal/service/app/config; this package owns
// only the contract + the typed failure sentinels.
//
// Package config — the optional self-validation contract.
//
// Package config — the change-observer contract.
package config
