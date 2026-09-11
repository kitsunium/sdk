// Package sql — range 0.2.24.* (ADR 0055 core/sql block).
package sql

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.24.0 - 0.2.24.255

// CodeUnknownDialect identifies a dialect name this SDK has never heard of.
const CodeUnknownDialect errs.Code = 0x00_02_18_01 // 0.2.24.1

// CodeDialectRefused identifies a dialect this SDK RECOGNISES and declines,
// because its savepoint grammar is a different algorithm rather than a
// different string. The reason travels in a field.
const CodeDialectRefused errs.Code = 0x00_02_18_02 // 0.2.24.2

// CodeNestedIsolation identifies a nested Transact that asked for an
// isolation level or read-only-ness a savepoint cannot provide.
const CodeNestedIsolation errs.Code = 0x00_02_18_03 // 0.2.24.3

// CodeInvalidMigration identifies a migration that could never run: version
// zero, an empty name, a nil Up, or a nil Down.
const CodeInvalidMigration errs.Code = 0x00_02_18_04 // 0.2.24.4

// CodeMigrationIrreversible identifies a Down refused because the caller
// declared the migration irreversible with the Irreversible step.
const CodeMigrationIrreversible errs.Code = 0x00_02_18_05 // 0.2.24.5
