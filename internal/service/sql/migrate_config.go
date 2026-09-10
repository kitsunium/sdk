// Package sql — hosts MigrateConfig, the migration runner's parameters, and
// the validation that turns a caller's slice into an ordered plan.
package sql

import (
	"regexp"
	"slices"
	"strconv"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultVersionTable is the table an empty [MigrateConfig.VersionTable]
// clamps to. The name matches the de-facto convention of every migration tool
// in the ecosystem, so an operator inspecting a database recognises it without
// being told.
const DefaultVersionTable string = "schema_migrations"

// DefaultLockTimeout is the migration-lock budget a non-positive
// [MigrateConfig.LockTimeout] clamps to.
//
// A migration run that waits forever for another holder is a deployment that
// hangs with no output, which is strictly worse than one that fails and says
// why. Two minutes is longer than almost every migration and shorter than
// every deployment timeout that would otherwise kill the pod first.
const DefaultLockTimeout time.Duration = 2 * time.Minute

// DefaultLockRetryInterval is how often a blocked runner retries the
// non-blocking lock acquisition. A non-positive
// [MigrateConfig.LockRetryInterval] clamps to it.
const DefaultLockRetryInterval time.Duration = 250 * time.Millisecond

// identifierPattern is the only shape accepted for a version-table name. A
// table name cannot be a bound parameter, so it is interpolated — and this
// pattern is the whole thing standing between a configuration string and an
// injection.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MigrateConfig parameterises [NewMigrator].
//
// It carries no directory, no file pattern and no naming convention, and the
// SDK ships no example migration tree. Migrations are values the consumer
// builds, because the moment the SDK reads a directory it has invented a
// convention every consumer must adopt (ADR 0055 §D8).
type MigrateConfig struct {
	// Migrations is the complete set this binary carries. Order does not
	// matter — the runner sorts by Version — but every entry must be valid
	// and no two may share a version.
	//
	// It is COPIED at construction, so a caller appending to their slice
	// afterwards cannot change what the runner will apply.
	Migrations []coresql.MigrationValue
	// VersionTable names the bookkeeping table. Empty clamps to
	// [DefaultVersionTable]; anything that is not a bare SQL identifier is
	// REFUSED, because it is interpolated rather than bound.
	VersionTable string
	// LockTimeout bounds the wait for the advisory lock. Non-positive clamps
	// to [DefaultLockTimeout].
	LockTimeout time.Duration
	// LockRetryInterval is how often the non-blocking acquisition is retried
	// while another holder has the lock. Non-positive clamps to
	// [DefaultLockRetryInterval].
	LockRetryInterval time.Duration
}

// resolve validates and normalises the migration configuration.
func (m MigrateConfig) resolve() (plan migratePlan, err error) {
	table, err := m.resolvedTable()
	//: an unusable table name is refused before any connection is touched.
	if err != nil {
		//: propagate VERSION_TABLE_INVALID unchanged.
		return migratePlan{}, err
	}
	//: the runner's own copy: a caller appending later changes nothing.
	sorted := slices.Clone(m.Migrations)
	//: ascending version order IS the application order.
	slices.SortFunc(sorted, func(a, b coresql.MigrationValue) int {
		//: cmp.Compare on uint64 without converting to a signed type.
		return compareVersions(a.Version, b.Version)
	})
	//: every migration must be runnable and uniquely numbered.
	if verr := validateSet(sorted); verr != nil {
		//: propagate INVALID_MIGRATION / DUPLICATE_MIGRATION unchanged.
		return migratePlan{}, verr
	}
	//: both budgets have documented clamps.
	return migratePlan{
		migrations: sorted, table: table,
		lockBudget: m.lockBudget(), retry: m.retryInterval(),
	}, nil
}

// resolvedTable applies the version-table clamp and validation.
func (m MigrateConfig) resolvedTable() (table string, err error) {
	name := m.VersionTable
	//: an unset name takes the ecosystem's own convention.
	if name == "" {
		//: the documented default.
		name = DefaultVersionTable
	}
	//: an identifier cannot be bound, so it must be proven safe here.
	if !identifierPattern.MatchString(name) {
		//: the offending name is the caller's own configuration, not a secret.
		return "", kerrs.Wrap(VersionTableInvalid, kerrs.WrapParams{}, kerrs.String("table", name))
	}
	//: safe to interpolate.
	return name, nil
}

// lockBudget applies the documented lock-timeout clamp.
func (m MigrateConfig) lockBudget() time.Duration {
	//: a non-positive budget is an unset field, never "wait forever".
	if m.LockTimeout <= 0 {
		//: the documented floor.
		return DefaultLockTimeout
	}
	//: the caller's own budget.
	return m.LockTimeout
}

// retryInterval applies the documented retry-interval clamp.
func (m MigrateConfig) retryInterval() time.Duration {
	//: a non-positive interval would busy-loop against the database.
	if m.LockRetryInterval <= 0 {
		//: the documented cadence.
		return DefaultLockRetryInterval
	}
	//: the caller's own cadence.
	return m.LockRetryInterval
}

// compareVersions orders two versions without a signed conversion that could
// wrap on a large timestamp-shaped value.
func compareVersions(a, b uint64) int {
	//: strictly less.
	if a < b {
		//: a comes first.
		return -1
	}
	//: strictly greater.
	if a > b {
		//: b comes first.
		return 1
	}
	//: equal — validateSet turns this into DUPLICATE_MIGRATION.
	return 0
}

// validateSet reports the first migration that could never run, or the first
// duplicated version.
func validateSet(sorted []coresql.MigrationValue) error {
	//: the slice is already ascending, so a duplicate is always adjacent.
	for index, migration := range sorted {
		//: a migration validates itself — the rule lives on the value.
		if err := migration.Validate(); err != nil {
			//: propagate INVALID_MIGRATION unchanged.
			return err
		}
		//: the first entry has no predecessor to collide with.
		if index == 0 {
			//: nothing to compare against.
			continue
		}
		//: the version IS the identity, so two of them make "has this been
		//: applied?" unanswerable.
		if sorted[index-1].Version == migration.Version {
			//: name the duplicated value; it is the caller's own.
			return kerrs.Wrap(DuplicateMigration, kerrs.WrapParams{},
				kerrs.String("version", strconv.FormatUint(migration.Version, decimalBase)))
		}
	}
	//: every migration is runnable and uniquely numbered.
	return nil
}
