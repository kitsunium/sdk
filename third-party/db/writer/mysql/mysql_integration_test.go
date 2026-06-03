//go:build integration

// Package mysql_test — live INSERT round-trip against a real MySQL 8 container.
//
// This test exercises the FULL registered-factory path: it builds the
// production "mysql" sink through writer.Open (blank-import side effect), writes
// records, drains them, and reads the rows back with a raw *sql.DB to prove the
// (ts, level, message) tuple survived the round-trip.
//
// The production writer connects over a MySQL Unix-domain socket (local-protocol
// policy — MySQLConfig exposes no TCP host/port). The container therefore
// bind-mounts a host directory onto /var/run/mysqld so the server's socket
// appears on the host filesystem; SocketPath points at it.
//
// Run (Docker required):
//
//	GOWORK=off go test -tags integration ./third-party/db/writer/mysql/...
//
// Without Docker the container Run fails and the test self-skips, so a default
// CI lane stays green.
package mysql_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/moby/moby/api/types/container"
	tc "github.com/testcontainers/testcontainers-go"
	mysqlc "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/third-party/db/writer/mysql"
)

// itCreds is a CredentialProvider returning the container's root login; the
// integration test needs a live credential the offline fakes cannot supply.
type itCreds struct {
	user string
	pass string
}

func (c itCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: AccessKeyID maps to the MySQL user, SecretAccessKey to the password.
	return writer.NewCredentialValue(c.user, c.pass, ""), nil
}

// startMySQL spins up mysql:8 with its socket directory bind-mounted to a host
// dir, returning the host socket path. It self-skips when Docker is unavailable.
func startMySQL(ctx context.Context, t *testing.T) (socket string) {
	t.Helper()
	//: a fresh host dir receives the server's Unix socket via the bind mount.
	sockDir := t.TempDir()
	//: world-writable so the in-container mysqld uid can create the socket.
	if cerr := os.Chmod(sockDir, 0o777); cerr != nil {
		t.Fatalf("chmod sockdir: %v", cerr)
	}
	ctr, err := mysqlc.Run(
		ctx, "mysql:8",
		mysqlc.WithDatabase("logs"),
		mysqlc.WithUsername("root"),
		mysqlc.WithPassword("root"),
		tc.CustomizeRequestOption(func(req *tc.GenericContainerRequest) error {
			//: bind the host socket dir onto the server's socket location.
			req.HostConfigModifier = func(hc *container.HostConfig) {
				hc.Binds = append(hc.Binds, sockDir+":/var/run/mysqld")
			}
			//: wait until the socket exists so the first connect cannot race.
			req.WaitingFor = wait.ForListeningPort("3306/tcp")
			return nil
		}),
	)
	//: no Docker daemon (or image pull blocked) → skip, keeping CI green.
	if err != nil {
		if ctr != nil {
			_ = tc.TerminateContainer(ctr)
		}
		t.Skipf("docker unavailable: %v", err)
	}
	t.Cleanup(func() {
		//: tear the container down after the case.
		if terr := tc.TerminateContainer(ctr); terr != nil {
			t.Errorf("terminate: %v", terr)
		}
	})
	return filepath.Join(sockDir, "mysqld.sock")
}

// waitSocket blocks until the bind-mounted socket file appears or the deadline
// elapses; the server creates it a moment after the port opens.
func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		//: the socket file is the live readiness signal for local-protocol.
		if _, serr := os.Stat(path); serr == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Skipf("mysql socket never appeared at %s (bind-mount unsupported here)", path)
}

func TestMySQLIntegration_WriteAndRead(t *testing.T) {
	ctx := context.Background()
	socket := startMySQL(ctx, t)
	waitSocket(t, socket)

	//: the operator-provided schema the writer documents (ts, level, message).
	rawDSN := buildRawDSN(socket)
	raw := mustOpenRaw(t, rawDSN)
	t.Cleanup(func() {
		//: release the verification handle.
		if cerr := raw.Close(); cerr != nil {
			t.Errorf("raw.Close: %v", cerr)
		}
	})
	mustExec(ctx, t, raw, "CREATE TABLE IF NOT EXISTS app_logs (ts DATETIME, level VARCHAR(16), message TEXT)")

	//: build the production sink through its REGISTERED factory (writer.Open).
	cfg := writer.MySQLConfig{
		SocketPath:  socket,
		Database:    "logs",
		Table:       "app_logs",
		Credentials: itCreds{user: "root", pass: "root"},
		FlushEvery:  100 * time.Millisecond,
	}
	sink, oerr := writer.Open("mysql", cfg)
	//: the registered factory must resolve and build a usable sink.
	if oerr != nil {
		t.Fatalf("writer.Open(mysql): %v", oerr)
	}

	//: three records with a distinguishable first message for the read-back.
	records := []corelogger.RecordEvent{
		{Time: time.Now().UTC().Truncate(time.Second), Level: level.Info, Message: "first integration line"},
		{Time: time.Now().UTC().Truncate(time.Second), Level: level.Warn, Message: "second integration line"},
		{Time: time.Now().UTC().Truncate(time.Second), Level: level.Error, Message: "third integration line"},
	}
	for _, r := range records {
		//: the producer never blocks; the drainer performs the INSERT.
		if _, werr := sink.Write(ctx, r, []byte(r.Message)); werr != nil {
			t.Fatalf("sink.Write: %v", werr)
		}
	}
	//: force the partial batch out, then drain + close the pool.
	if ferr := sink.Flush(ctx); ferr != nil {
		t.Fatalf("sink.Flush: %v", ferr)
	}
	if cerr := sink.Close(); cerr != nil {
		t.Fatalf("sink.Close: %v", cerr)
	}

	//: all three rows must have landed via the full registered path.
	var count int
	if qerr := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_logs").Scan(&count); qerr != nil {
		t.Fatalf("count query: %v", qerr)
	}
	if count != len(records) {
		t.Fatalf("row count=%d want %d", count, len(records))
	}

	//: the INFO row must round-trip its level + message verbatim.
	var gotLevel, gotMsg string
	row := raw.QueryRowContext(ctx, "SELECT level, message FROM app_logs WHERE level = ? LIMIT 1", level.Info.String())
	if serr := row.Scan(&gotLevel, &gotMsg); serr != nil {
		t.Fatalf("select INFO row: %v", serr)
	}
	if gotLevel != level.Info.String() || gotMsg != records[0].Message {
		t.Errorf("round-trip mismatch: level=%q message=%q want %q/%q", gotLevel, gotMsg, level.Info.String(), records[0].Message)
	}
}

// buildRawDSN formats a net=unix verification DSN at the same socket the writer
// uses, with ParseTime so DATETIME scans into time.Time.
func buildRawDSN(socket string) string {
	cfg := driver.NewConfig()
	//: match the writer's transport: the Unix domain socket, never TCP.
	cfg.Net = "unix"
	cfg.Addr = socket
	cfg.DBName = "logs"
	cfg.User = "root"
	cfg.Passwd = "root"
	//: scan DATETIME into time.Time on the read path.
	cfg.ParseTime = true
	return cfg.FormatDSN()
}

// mustOpenRaw opens a raw verification handle and fails the test on a lazy-open
// error (a malformed DSN).
func mustOpenRaw(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	//: sql.Open is lazy — a failure here means a malformed DSN.
	if err != nil {
		t.Fatalf("sql.Open(raw): %v", err)
	}
	return db
}

// mustExec runs a statement on the raw handle, failing the test on error. Used
// for the one-time schema setup that the operator owns in production.
func mustExec(ctx context.Context, t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	//: the schema setup must succeed before any sink write is read back.
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}
