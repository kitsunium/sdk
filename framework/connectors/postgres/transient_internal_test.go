package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestTransientReadsTheSQLSTATE is the table of #1000: SQLSTATE 40001 and
// 40P01 run again, anything else does not, joined or wrapped.
func TestTransientReadsTheSQLSTATE(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"serialization failure 40001", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock detected 40P01", &pgconn.PgError{Code: "40P01"}, true},
		{"unique violation 23505", &pgconn.PgError{Code: "23505"}, false},
		{"joined beside a verdict", errors.Join(errors.ErrUnsupported, &pgconn.PgError{Code: "40001"}), true},
		{"not a driver error", errors.ErrUnsupported, false},
		{"nil", nil, false},
	} {
		if got := (engine{}).Transient(c.err); got != c.want {
			t.Errorf("%s: Transient = %t, want %t", c.name, got, c.want)
		}
	}
}
