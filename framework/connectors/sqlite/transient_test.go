package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// coded is an error naming a SQLite result code, as the driver's does.
type coded int

func (c coded) Error() string { return "sqlite result code" }
func (c coded) Code() int     { return int(c) }

// TestTransientReadsSQLitesResultCode is the table of #1000 — busy, locked
// and their extended forms run again — and the driver's own busy, met for
// real: a second writer refused while the first holds the file.
func TestTransientReadsSQLitesResultCode(t *testing.T) {
	classifier, ok := sqlite.Engine().(app.Classifier)
	if !ok {
		t.Fatal("the engine reads no driver error")
	}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"busy 5", coded(5), true},
		{"locked 6", coded(6), true},
		{"busy snapshot 517", coded(517), true},
		{"constraint 19", coded(19), false},
		{"joined", errors.Join(errors.ErrUnsupported, coded(5)), true},
		{"not a driver error", errors.ErrUnsupported, false},
	} {
		if got := classifier.Transient(c.err); got != c.want {
			t.Errorf("%s: Transient = %t, want %t", c.name, got, c.want)
		}
	}
	path := filepath.Join(t.TempDir(), "busy.sqlite")
	v := secret.FromString("file:" + path + "?_busy_timeout=0")
	first, err := sqlite.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := sqlite.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.ExecContext(t.Context(), "CREATE TABLE t (n INT)"); err != nil {
		t.Fatal(err)
	}
	holding, err := first.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holding.Rollback()
	_, busy := second.BeginTx(t.Context(), nil)
	if busy == nil || !classifier.Transient(busy) {
		t.Fatalf("a second writer's BEGIN = %v, want SQLite's busy, read as transient", busy)
	}
}
