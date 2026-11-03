// Package sql_test — migration checksums (#1056): an applied migration whose
// statements were edited is refused before anything runs, a version table from
// an earlier release gains its column under the lock, and a set that declares
// no checksum sends what it always sent.
package sql_test

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// The version table's checksum statements, spelled out as the runner sends
// them on PostgreSQL.
const (
	probeChecksum   = "SELECT checksum FROM schema_migrations WHERE 1 = 0"
	addChecksum     = "ALTER TABLE schema_migrations ADD COLUMN checksum TEXT"
	readChecksums   = "SELECT version, checksum FROM schema_migrations ORDER BY version"
	recordChecksum  = "INSERT INTO schema_migrations (version, name, applied_at_unix, checksum) VALUES ($1, $2, $3, $4)"
	backfillChecked = "UPDATE schema_migrations SET checksum = $1 WHERE version = $2 AND checksum IS NULL"
)

// recordedRows scripts the checksum read to answer the given (version,
// checksum) rows, a nil checksum being a NULL.
func recordedRows(f *fakeDB, rows ...[]driver.Value) *fakeDB {
	return f.on(readChecksums, func(int) ([]string, [][]driver.Value, error) {
		return []string{"version", "checksum"}, rows, nil
	})
}

// checked is a migration built from statements, its checksum declared from
// them as the doc of ChecksumOf asks.
func checked(log *[]string, version uint64, stmts ...string) coresql.MigrationValue {
	return coresql.MigrationValue{
		Version: version, Name: "v" + strings.Repeat("i", int(version)),
		Up: func(ctx context.Context, ex coresql.Executor) error {
			*log = append(*log, stmts...)
			return svcsql.Statements(stmts...)(ctx, ex)
		},
		Down:     coresql.Irreversible,
		Checksum: svcsql.ChecksumOf(stmts...),
	}
}

// TestChecksumOfIsTheLengthPrefixedSHA256 pins the fingerprint's definition
// byte for byte, and that the length prefix keeps two statements from being
// read as their concatenation.
func TestChecksumOfIsTheLengthPrefixedSHA256(t *testing.T) {
	t.Parallel()
	digest := sha256.New()
	for _, stmt := range []string{"CREATE TABLE a", "(x INT)"} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(stmt)))
		digest.Write(n[:])
		digest.Write([]byte(stmt))
	}
	want := hex.EncodeToString(digest.Sum(nil))
	if got := svcsql.ChecksumOf("CREATE TABLE a", "(x INT)"); got != want {
		t.Fatalf("ChecksumOf = %s, want %s", got, want)
	}
	if svcsql.ChecksumOf("CREATE TABLE a", "(x INT)") == svcsql.ChecksumOf("CREATE TABLE a(x INT)") {
		t.Fatal("two statements and their concatenation share a checksum")
	}
	if got := svcsql.ChecksumOf(); len(got) != 64 || strings.ToLower(got) != got {
		t.Fatalf("ChecksumOf() = %q, want 64 lowercase hex digits", got)
	}
}

// TestAnEditedAppliedMigrationIsRefusedBeforeAnythingApplies is the first
// acceptance: editing an applied Statements migration's text makes Up and
// Plan return MIGRATION_CHANGED, with the version and the direction, and
// apply nothing — the pending migration after it included.
func TestAnEditedAppliedMigrationIsRefusedBeforeAnythingApplies(t *testing.T) {
	t.Parallel()
	f := recordedRows(grantLock(newFakeDB()),
		[]driver.Value{int64(1), svcsql.ChecksumOf("CREATE TABLE accounts (id BIGINT)")})
	var ran []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		checked(&ran, 1, "CREATE TABLE accounts (id BIGINT, cents BIGINT)"),
		checked(&ran, 2, "CREATE TABLE ledger (id BIGINT)"),
	)
	for name, run := range map[string]func() error{
		"Up":   func() error { return runner.Up(t.Context()) },
		"Plan": func() error { _, err := runner.Plan(t.Context()); return err },
	} {
		err := run()
		if !errs.HasCode(err, coresql.CodeMigrationChanged) {
			t.Fatalf("%s = %v, want MIGRATION_CHANGED", name, err)
		}
		e := verdictOf(err, coresql.CodeMigrationChanged)
		if !hasFieldValue(e, "version", "1") || !hasFieldValue(e, "direction", "up") {
			t.Fatalf("%s = %v, want the fields version=1 direction=up", name, err)
		}
		if strings.Contains(err.Error(), "cents") {
			t.Fatalf("%s = %q, which quotes the migration's text", name, err)
		}
	}
	if len(ran) != 0 || f.sent(recordChecksum) {
		t.Fatalf("ran %v and recorded=%t, want nothing applied", ran, f.sent(recordChecksum))
	}
}

// TestTheSameSetUneditedAppliesThePendingOnes is the other half of the first
// acceptance: a matching checksum is no refusal, and the pending migration is
// recorded with its own.
func TestTheSameSetUneditedAppliesThePendingOnes(t *testing.T) {
	t.Parallel()
	f := recordedRows(grantLock(newFakeDB()),
		[]driver.Value{int64(1), svcsql.ChecksumOf("CREATE TABLE accounts (id BIGINT)")})
	var ran []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		checked(&ran, 1, "CREATE TABLE accounts (id BIGINT)"),
		checked(&ran, 2, "CREATE TABLE ledger (id BIGINT)"),
	)
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if want := []string{"CREATE TABLE ledger (id BIGINT)"}; !slices.Equal(ran, want) {
		t.Fatalf("ran %v, want only the pending migration %v", ran, want)
	}
	if !f.sent(recordChecksum) || f.sent(recordRow) {
		t.Fatal("the pending migration was not recorded with its checksum")
	}
	if f.sent(addChecksum) {
		t.Fatal("the column was added to a table the probe found it on")
	}
	if !f.sent("CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT NOT NULL PRIMARY KEY, " +
		"name TEXT NOT NULL, applied_at_unix BIGINT NOT NULL, checksum TEXT)") {
		t.Fatalf("statements %v, want the four-column definition for a set that declares checksums", f.statements())
	}
}

// TestAThreeColumnTableGainsTheColumnUnderTheLockOnly is the second
// acceptance on the scripted driver: an earlier release's table — the probe
// refused — gets the column from Up, under the lock, and keeps its rows; Plan,
// which takes no lock and applies nothing, reads it as unverified instead.
func TestAThreeColumnTableGainsTheColumnUnderTheLockOnly(t *testing.T) {
	t.Parallel()
	planned := grantLock(newFakeDB()).failOn(probeChecksum)
	var ran []string
	runner, _ := newMigrator(t, planned, clock.NewManualClock(time.Unix(0, 0)),
		checked(&ran, 1, "CREATE TABLE accounts (id BIGINT)"))
	if _, err := runner.Plan(t.Context()); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planned.sent(addChecksum) || planned.sent(readChecksums) || !planned.sent(readApplied) {
		t.Fatalf("Plan sent %v, want the three-column read and no ALTER", planned.statements())
	}
	upped := grantLock(newFakeDB()).failOn(probeChecksum)
	runner, _ = newMigrator(t, upped, clock.NewManualClock(time.Unix(0, 0)),
		checked(&ran, 1, "CREATE TABLE accounts (id BIGINT)"))
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	log := upped.statements()
	alter, read, lock := slices.Index(log, addChecksum), slices.Index(log, readChecksums), slices.Index(log, tryLock)
	if alter < 0 || lock < 0 || alter < lock || read < alter {
		t.Fatalf("Up sent %v, want the ALTER under the lock, before the checksum read", log)
	}
}

// TestAVersionRecordedWithoutAChecksumIsBackfilled pins that a version an
// earlier release recorded — NULL in the column — is given the checksum the
// set declares by the next locked run, and is not called changed.
func TestAVersionRecordedWithoutAChecksumIsBackfilled(t *testing.T) {
	t.Parallel()
	f := recordedRows(grantLock(newFakeDB()), []driver.Value{int64(1), nil})
	var ran []string
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)),
		checked(&ran, 1, "CREATE TABLE accounts (id BIGINT)"))
	if err := runner.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !f.sent(backfillChecked) {
		t.Fatalf("statements %v, want the backfill", f.statements())
	}
	if len(ran) != 0 {
		t.Fatalf("ran %v, want nothing: version 1 is applied", ran)
	}
}

// TestAnEmptyChecksumIsNeverComparedAndDownSaysItsDirection pins the third
// acceptance's first half — a migration that declares nothing is not
// compared, whatever its row recorded — and that Down refuses a changed one
// with its own direction.
func TestAnEmptyChecksumIsNeverComparedAndDownSaysItsDirection(t *testing.T) {
	t.Parallel()
	f := recordedRows(grantLock(newFakeDB()),
		[]driver.Value{int64(1), "recorded by another build"},
		[]driver.Value{int64(2), svcsql.ChecksumOf("CREATE TABLE b (id BIGINT)")})
	var ran []string
	unverified := coresql.MigrationValue{
		Version: 1, Name: "code", Up: step(&ran, "UP 1"), Down: step(&ran, "DOWN 1"),
	}
	edited := checked(&ran, 2, "CREATE TABLE b (id BIGINT, c TEXT)")
	edited.Down = step(&ran, "DOWN 2")
	runner, _ := newMigrator(t, f, clock.NewManualClock(time.Unix(0, 0)), unverified, edited)
	err := runner.Down(t.Context(), 0)
	if !errs.HasCode(err, coresql.CodeMigrationChanged) {
		t.Fatalf("Down = %v, want MIGRATION_CHANGED for version 2", err)
	}
	e := verdictOf(err, coresql.CodeMigrationChanged)
	if !hasFieldValue(e, "version", "2") || !hasFieldValue(e, "direction", "down") {
		t.Fatalf("Down = %v, want the fields version=2 direction=down", err)
	}
	if len(ran) != 0 {
		t.Fatalf("Down reversed %v before refusing", ran)
	}
}

// verdictOf is the *errs.Error carrying code in err's tree, nil when none
// does: a run's verdict travels joined beside the lock's release.
func verdictOf(err error, code errs.Code) *errs.Error {
	if e, ok := err.(*errs.Error); ok && errs.HasCode(e, code) {
		return e
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if e := verdictOf(cause, code); e != nil {
				return e
			}
		}
	}
	return nil
}

// hasFieldValue reports whether e carries the field key=value.
func hasFieldValue(e *errs.Error, key, value string) bool {
	if e == nil {
		return false
	}
	for _, field := range e.Fields() {
		if field.Key() == key && field.StringValue() == value {
			return true
		}
	}
	return false
}
