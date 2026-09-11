// Package sql — hosts MigrationValue and the Step both of its halves are.
package sql

import (
	"context"
	"math"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// decimalBase is the radix a version is rendered in when it travels as an
// error field. Named because 10 appearing four times in one file is exactly
// the kind of literal that gets "fixed" to 16 by a well-meaning edit.
const decimalBase int = 10

// Step is one direction of a migration: the statements that move the schema.
//
// A FUNCTION port, for the same reason [TxFunc] is: one behaviour, so a named
// func IS the contract, and a func type cannot grow a method (ADR 0039).
//
// It receives the SAME [Executor] the surrounding transaction runs on, which
// is what makes a migration and its version-table row commit together.
type Step func(ctx context.Context, ex Executor) error

// MigrationValue is one versioned schema change and its reversal.
//
// The SDK does NOT own a migration FILE FORMAT, a directory layout, or a
// naming convention, and it ships no example directory. A migration is a value
// the consumer constructs, because the moment the SDK reads a directory it has
// invented a convention every consumer must adopt and no consumer asked for
// (ADR 0055 §D8). Where the statements come from — a Go literal, an embed.FS,
// a generator — is the caller's decision and none of the SDK's business.
//
// The zero value is not runnable and is refused at construction.
type MigrationValue struct {
	// Version orders the migration. It must be non-zero and unique within one
	// set: zero is reserved as "before every migration", the value Down takes
	// to reverse everything.
	//
	// Any strictly-increasing scheme works; a UTC timestamp (20260910143000)
	// is the conventional one because two branches developed in parallel then
	// merge without colliding.
	Version uint64
	// Name is the human label recorded in the version table. It must be
	// non-empty: a schema history whose rows say only "42" is a schema
	// history nobody can read during an incident.
	Name string
	// Up applies the change. Required.
	Up Step
	// Down reverses it. Required — and [Irreversible] is how a caller says
	// "this one cannot be reversed" OUT LOUD.
	//
	// A nil Down is refused rather than accepted as "irreversible", because
	// "I forgot the reversal" and "there is no reversal" are the same nil and
	// only one of them is a defect. The same rule core/lifecycle applies to a
	// nil Stop, for the same reason.
	Down Step
}

// Irreversible is the [Step] a caller assigns to [MigrationValue.Down] to
// declare that the migration cannot be reversed. It always fails, with a typed
// error naming the version.
//
// It is a value rather than a nil convention so the claim appears in the diff
// and in the version-table review, and so `Down` reports a refusal the caller
// wrote rather than a nil dereference the SDK discovered.
func Irreversible(ctx context.Context, ex Executor) error {
	//: neither half is read: the refusal is unconditional, and the signature
	//: is fixed by [Step], which this value must satisfy.
	_, _ = ctx, ex
	//: the refusal is the whole behaviour; the runner adds the version field.
	return MigrationIrreversible
}

// Validate reports why the migration could never run, or nil.
//
// It is a method on the value rather than a check inside the runner so a
// caller can validate a set at start-up — before any connection exists —
// which is where a wiring fault should be found.
func (m MigrationValue) Validate() error {
	//: version 0 is reserved for Down's "reverse everything" target, so a
	//: migration can never legitimately claim it.
	if m.Version == 0 {
		//: name the rule that failed, never the SQL.
		return errs.Wrap(InvalidMigration, errs.WrapParams{},
			errs.String("missing", "version"))
	}
	//: a version is stored in a BIGINT and bound as an int64, because that is
	//: what BIGINT is on every engine this domain speaks. A value above the
	//: signed ceiling has no representation there, and converting it would
	//: silently reorder the history — so it is refused here, where the fix is
	//: a renumbering rather than a data-loss investigation.
	if m.Version > math.MaxInt64 {
		//: the offending number is the caller's own.
		return errs.Wrap(InvalidMigration, errs.WrapParams{},
			errs.String("version", strconv.FormatUint(m.Version, decimalBase)),
			errs.String("missing", "version-fits-int64"))
	}
	//: an unnamed migration is unreadable in the version table.
	if m.Name == "" {
		//: the field says which half is absent.
		return errs.Wrap(InvalidMigration, errs.WrapParams{},
			errs.String("version", strconv.FormatUint(m.Version, decimalBase)), errs.String("missing", "name"))
	}
	//: a migration with no Up is not a migration.
	if m.Up == nil {
		//: same shape, different half.
		return errs.Wrap(InvalidMigration, errs.WrapParams{},
			errs.String("version", strconv.FormatUint(m.Version, decimalBase)), errs.String("missing", "up"))
	}
	//: a nil Down is a forgotten reversal; Irreversible is the deliberate one.
	if m.Down == nil {
		//: point the caller at the value that states the claim out loud.
		return errs.Wrap(InvalidMigration, errs.WrapParams{},
			errs.String("version", strconv.FormatUint(m.Version, decimalBase)), errs.String("missing", "down"))
	}
	//: every half is present.
	return nil
}
