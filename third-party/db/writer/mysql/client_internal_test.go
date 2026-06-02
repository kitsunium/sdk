package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// staticCreds is a CredentialProvider returning fixed login material for the
// happy-path white-box tests.
type staticCreds struct{}

func (staticCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: fixed user/password drives the DSN-building path deterministically.
	return writer.NewCredentialValue("logger", "s3cr3t", ""), nil
}

// failCreds is a CredentialProvider that always fails, exercising the
// credential-resolution error branch.
type failCreds struct{}

func (failCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: a fixed failure drives the credential-error branch.
	return writer.CredentialValue{}, errs.Wrap(nil, errs.WrapParams{
		Code: CodeMySQLClientInitFailed, Reason: "CLIENT_INIT_FAILED",
		Public: "no creds", Private: "test failCreds",
	})
}

func Test_newClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.MySQLConfig
		wantErr bool
	}{
		{"nil credentials rejected", writer.MySQLConfig{Table: "logs"}, true},
		{"invalid table rejected", writer.MySQLConfig{Table: "bad table;", Credentials: staticCreds{}}, true},
		{"credential failure rejected", writer.MySQLConfig{Table: "logs", Credentials: failCreds{}}, true},
		{"valid config opens a lazy handle", writer.MySQLConfig{SocketPath: "/tmp/m.sock", Database: "d", Table: "logs", Credentials: staticCreds{}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, exec, err := newClient(tc.cfg)
			//: error arm — client-init sentinel, nil handle + closure.
			if tc.wantErr {
				if db != nil || exec != nil || !errs.HasCode(err, CodeMySQLClientInitFailed) {
					t.Fatalf("%s: db=%v execNil=%v err=%v want nil+client-init", tc.name, db, exec == nil, err)
				}
				return
			}
			//: happy arm — a lazy handle + a non-nil deliver closure.
			if err != nil || db == nil || exec == nil {
				t.Fatalf("%s: db=%v execNil=%v err=%v want handle+closure", tc.name, db, exec == nil, err)
			}
			//: release the lazily-opened pool (no connection was made).
			if cerr := db.Close(); cerr != nil {
				t.Errorf("%s: db.Close: %v", tc.name, cerr)
			}
		})
	}
}

func Test_buildDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.MySQLConfig
		needles []string
	}{
		{"unix socket dsn", writer.MySQLConfig{SocketPath: "/tmp/m.sock", Database: "logs"}, []string{"unix(", "/tmp/m.sock", "logs", "logger:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dsn := buildDSN(tc.cfg, "logger", "s3cr3t")
			//: the formatted DSN must select net=unix at the socket path.
			for _, needle := range tc.needles {
				if !strings.Contains(dsn, needle) {
					t.Errorf("%s: dsn %q missing %q", tc.name, dsn, needle)
				}
			}
		})
	}
}

func Test_buildInsert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		batch    []corelogger.RecordEvent
		wantArgs int
		needles  []string
	}{
		{"two rows", []corelogger.RecordEvent{{Message: "a", Level: level.Info}, {Message: "b", Level: level.Warn}}, 6, []string{"INSERT INTO logs (ts, level, message) VALUES ", "(?,?,?),(?,?,?)"}},
		{"one row", []corelogger.RecordEvent{{Message: "x", Level: level.Error, Time: time.Unix(0, 0)}}, 3, []string{"(?,?,?)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			query, args := buildInsert("logs", tc.batch)
			//: the arg list flattens to colsPerRow per record.
			if len(args) != tc.wantArgs {
				t.Errorf("%s: len(args)=%d want %d", tc.name, len(args), tc.wantArgs)
			}
			//: the rendered statement must contain the expected fragments.
			for _, needle := range tc.needles {
				if !strings.Contains(query, needle) {
					t.Errorf("%s: query %q missing %q", tc.name, query, needle)
				}
			}
		})
	}
}

func Test_isIdentRune(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"letter", 'a', true},
		{"digit", '7', true},
		{"underscore", '_', true},
		{"space rejected", ' ', false},
		{"semicolon rejected", ';', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: only the identifier alphabet is accepted.
			if got := isIdentRune(tc.r); got != tc.want {
				t.Errorf("%s: isIdentRune(%q)=%v want %v", tc.name, tc.r, got, tc.want)
			}
		})
	}
}

func Test_validIdent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain identifier", "app_logs", true},
		{"empty rejected", "", false},
		{"space rejected", "app logs", false},
		{"semicolon rejected", "logs;drop", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: only plain identifiers are safe to interpolate into SQL.
			if got := validIdent(tc.in); got != tc.want {
				t.Errorf("%s: validIdent(%q)=%v want %v", tc.name, tc.in, got, tc.want)
			}
		})
	}
}
