package clickhouse

import (
	"context"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// staticCreds is a CredentialProvider returning fixed login material.
type staticCreds struct{}

func (staticCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: fixed user/password drives the auth-mapping path.
	return writer.NewCredentialValue("logger", "s3cr3t", ""), nil
}

// failCreds is a CredentialProvider that always fails.
type failCreds struct{}

func (failCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: a fixed failure drives the credential-error branch.
	return writer.CredentialValue{}, errs.Wrap(nil, errs.WrapParams{
		Code: CodeCHClientInitFailed, Reason: "CLIENT_INIT_FAILED",
		Public: "no creds", Private: "test failCreds",
	})
}

func Test_newClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.ClickHouseConfig
		wantErr bool
	}{
		{"invalid table rejected", writer.ClickHouseConfig{Address: "h:9000", Table: "bad;"}, true},
		{"credential failure rejected", writer.ClickHouseConfig{Address: "h:9000", Table: "logs", Credentials: failCreds{}}, true},
		{"valid config opens a lazy handle", writer.ClickHouseConfig{Address: "h:9000", Database: "d", Table: "logs", Credentials: staticCreds{}}, false},
		{"valid without creds", writer.ClickHouseConfig{Address: "h:9000", Database: "d", Table: "logs"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, exec, err := newClient(tc.cfg)
			//: error arm — client-init sentinel, nil handle + closure.
			if tc.wantErr {
				if db != nil || exec != nil || !errs.HasCode(err, CodeCHClientInitFailed) {
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

func Test_execClosure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// closeBefore forces ExecContext to fail synchronously by closing the
		// lazily-opened pool before exec is called.
		closeBefore bool
		batch       []corelogger.RecordEvent
		wantCode    errs.Code
	}{
		//: a nil batch short-circuits on the len==0 guard before any DB call.
		{"nil batch is a no-op", false, nil, 0},
		//: an empty slice hits the same len==0 guard.
		{"empty slice is a no-op", false, []corelogger.RecordEvent{}, 0},
		//: a non-empty batch over a closed pool reaches wrapInsert.
		{"insert failure wraps as InsertFailed", true, []corelogger.RecordEvent{{Message: "fail", Level: level.Error}}, CodeCHInsertFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a lazy handle never connects, so neither arm needs a live server.
			db, exec, err := newClient(writer.ClickHouseConfig{Address: "localhost:9999", Database: "d", Table: "logs"})
			if err != nil {
				t.Fatalf("%s: newClient: %v", tc.name, err)
			}
			//: closing the pool up front turns the next Exec into a synchronous
			//: "sql: database is closed" failure — the only arm that touches the DB.
			if tc.closeBefore {
				if cerr := db.Close(); cerr != nil {
					t.Fatalf("%s: db.Close: %v", tc.name, cerr)
				}
			} else {
				t.Cleanup(func() {
					//: release the still-open lazy pool after the case.
					if cerr := db.Close(); cerr != nil {
						t.Errorf("%s: cleanup db.Close: %v", tc.name, cerr)
					}
				})
			}
			eerr := exec(t.Context(), tc.batch)
			//: no-op arm — the guard returns nil without touching the DB.
			if tc.wantCode == 0 {
				if eerr != nil {
					t.Errorf("%s: want nil err, got %v", tc.name, eerr)
				}
				return
			}
			//: failure arm — the insert sentinel surfaces (identity, not string).
			if !errs.HasCode(eerr, tc.wantCode) {
				t.Errorf("%s: want code %v, got %v", tc.name, tc.wantCode, eerr)
			}
		})
	}
}

func Test_resolveCreds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cp       writer.CredentialProvider
		wantUser string
		wantErr  bool
	}{
		{"nil provider yields default user", nil, "", false},
		{"static provider yields login", staticCreds{}, "logger", false},
		{"failing provider errors", failCreds{}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user, _, err := resolveCreds(tc.cp)
			//: error arm — a provider failure surfaces.
			if (err != nil) != tc.wantErr {
				t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
			}
			//: happy arm — the username maps from AccessKeyID().
			if !tc.wantErr && user != tc.wantUser {
				t.Errorf("%s: user=%q want %q", tc.name, user, tc.wantUser)
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
		{"one row", []corelogger.RecordEvent{{Message: "x", Level: level.Error}}, 3, []string{"(?,?,?)"}},
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

func Test_validIdent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain identifier", "app_logs", true},
		{"empty rejected", "", false},
		{"semicolon rejected", "logs;drop", false},
		{"uppercase letter accepted", "Logs", true},
		{"mixed case accepted", "AppLogs", true},
		//: digits-only is a legal identifier under the current alphabet contract.
		{"digit only accepted", "123", true},
		{"dash rejected", "app-logs", false},
		{"dot rejected", "app.logs", false},
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
