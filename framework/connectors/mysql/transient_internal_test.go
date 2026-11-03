package mysql

import (
	"errors"
	"testing"

	driver "github.com/go-sql-driver/mysql"
)

// TestTransientReadsMySQLsErrorNumbers is the table of #1000: a deadlock and
// a lock wait timeout run again, anything else does not, joined or wrapped.
func TestTransientReadsMySQLsErrorNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock 1213", &driver.MySQLError{Number: 1213}, true},
		{"lock wait timeout 1205", &driver.MySQLError{Number: 1205}, true},
		{"duplicate entry 1062", &driver.MySQLError{Number: 1062}, false},
		{"joined beside a verdict", errors.Join(errors.ErrUnsupported, &driver.MySQLError{Number: 1213}), true},
		{"not a driver error", errors.ErrUnsupported, false},
		{"nil", nil, false},
	} {
		if got := (engine{}).Transient(c.err); got != c.want {
			t.Errorf("%s: Transient = %t, want %t", c.name, got, c.want)
		}
	}
}
