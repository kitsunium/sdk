// Package config — range 0.2.10.* (ADR 0028 core/app/config block).
//
// Package config — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package config — provenance: which layer supplied a key's final value, and
// the sibling a Source implements to say so.
//
// Package config — the schema's declared value: one key and what it holds when
// nobody supplied it.
//
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
