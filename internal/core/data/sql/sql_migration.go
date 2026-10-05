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

// irreversible is Irreversible's body: decl_gen.go writes Irreversible, from the
// design, as one call of it.
func irreversible(ctx context.Context, ex Executor) error {
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
