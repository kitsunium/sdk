package kit

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// migrationIrreversiblePublic is what an irreversible migration's Down says.
const migrationIrreversiblePublic = "the migration cannot be reversed: undo it with a new forward migration"

// MigrationValue is one versioned change of a database's schema and its
// reversal, kit's own (ADR 0179 step 10): the shape the SDK's sql runs,
// which the store subsystem hands it.
//
//	kit.Migration{
//		Version: 20261017120000,
//		Name:    "notes",
//		Up:      kit.Statements("CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT)"),
//		Down:    kit.Statements("DROP TABLE notes"),
//	}
type MigrationValue struct {
	// Version orders the migration in its set: non-zero and unique; a UTC
	// timestamp is the convention, so that two branches do not collide.
	Version uint64
	// Name labels it in the version table; required.
	Name string
	// Up applies the change; required.
	Up MigrationStep
	// Down reverses it; required — [Irreversible] says out loud that it
	// cannot be reversed.
	Down MigrationStep
}

// MigrationStep is one direction of a migration, run on ex — the
// transaction its version row is written in, so that both commit
// together.
type MigrationStep func(ctx context.Context, ex MigrationExecutor) error

// Statements is the [MigrationStep] that runs stmts in order on its
// executor, stopping at the first that fails — whose error it returns as
// the driver said it.
func Statements(stmts ...string) MigrationStep {
	return func(ctx context.Context, ex MigrationExecutor) error {
		for _, stmt := range stmts {
			//: the driver's error travels as it is: the SDK's migrator words
			//: the step's failure around it, with the version and direction.
			if _, err := ex.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
}

// Irreversible is the Down of a migration that cannot be reversed, said out
// loud rather than left nil: it refuses, with [CodeMigrationIrreversible],
// and `<product> migrate down` below its version stops there.
func Irreversible(ctx context.Context, ex MigrationExecutor) error {
	//: neither is read: the refusal is the whole step, and its signature is
	//: MigrationStep's.
	_, _ = ctx, ex
	return errs.New(CodeMigrationIrreversible, "MIGRATION_IRREVERSIBLE", migrationIrreversiblePublic,
		"kit: the migration's Down is kit.Irreversible: reversing it takes a new forward migration")
}

// pluggedMigrations are ms as the store subsystem runs them: the same
// versions, names and steps, each step on the executor it hands them.
func pluggedMigrations(ms []MigrationValue) []plug.MigrationValue {
	if ms == nil {
		return nil
	}
	out := make([]plug.MigrationValue, len(ms))
	for i, m := range ms {
		out[i] = plug.MigrationValue{Version: m.Version, Name: m.Name, Up: pluggedStep(m.Up), Down: pluggedStep(m.Down)}
	}
	return out
}

// pluggedStep is s on the store subsystem's executor, nil for none — which
// the SDK's migrator refuses, as it refuses a step left out.
func pluggedStep(s MigrationStep) plug.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex plug.Executor) error { return s(ctx, ex) }
}
