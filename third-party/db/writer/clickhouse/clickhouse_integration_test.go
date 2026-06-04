//go:build integration

// Package clickhouse — ClickHouse container integration test (L3). Excluded from
// the default build by the `integration` tag; requires Docker (or a Docker-compat
// runtime). When no runtime is reachable the test self-skips so default CI stays
// green. Run with:
//
//	GOWORK=off go test -tags integration -timeout 180s \
//	    ./third-party/db/writer/clickhouse/...
//
// It spins up clickhouse/clickhouse-server:24-alpine via the testcontainers-go
// clickhouse module, creates the documented three-column table, then drives the
// REAL registered factory (writer.Open("clickhouse", …)) end-to-end:
//
//  1. Open      → a *chSink built through clickhouseFactory.Open.
//  2. Write N   → records pushed through the production gate/async/batcher chain.
//  3. Flush     → synchronous drain of the async ring.
//  4. Close     → drains the chain, closes the pool.
//  5. SELECT    → a raw clickhouse-go client reads the rows back and asserts the
//     ts / level / message round-trip survived intact.
package clickhouse_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/third-party/db/writer/clickhouse" // self-registers the factory
)

// itCreds is a CredentialProvider that hands the container's default login to the
// production sink (AccessKeyID → username, SecretAccessKey → password).
type itCreds struct {
	user, password string
}

func (c itCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: the container's fixed user/password drives the real auth-mapping path.
	return writer.NewCredentialValue(c.user, c.password, ""), nil
}

// chFixture bundles a started container's native address with its login.
type chFixture struct {
	addr     string
	database string
	user     string
	password string
}

// startClickHouse boots clickhouse/clickhouse-server:24-alpine and returns its
// native-protocol address plus credentials. A Run error means no Docker-compat
// runtime is available, so the caller skips rather than fails.
func startClickHouse(t *testing.T) chFixture {
	t.Helper()
	ctx := t.Context()
	container, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24-alpine")
	//: no reachable container runtime → skip so default CI without Docker is green.
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	t.Cleanup(func() {
		//: a background ctx so teardown still runs after the test ctx is cancelled.
		if terr := container.Terminate(context.Background()); terr != nil {
			t.Errorf("terminate container: %v", terr)
		}
	})
	host, err := container.ConnectionHost(ctx)
	if err != nil {
		t.Fatalf("connection host: %v", err)
	}
	return chFixture{addr: host, database: container.DbName, user: container.User, password: container.Password}
}

// rawClient opens a lazy clickhouse-go handle for DDL + read-back assertions,
// reusing the same driver the production seam confines to client.go.
func rawClient(t *testing.T, f chFixture) chdriver.Conn {
	t.Helper()
	conn, err := chdriver.Open(&chdriver.Options{
		Addr: []string{f.addr},
		Auth: chdriver.Auth{Database: f.database, Username: f.user, Password: f.password},
	})
	if err != nil {
		t.Fatalf("open raw client: %v", err)
	}
	t.Cleanup(func() {
		//: release the read-back connection after the case.
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close raw client: %v", cerr)
		}
	})
	return conn
}

// createTable runs the documented three-column DDL against the container.
func createTable(t *testing.T, conn chdriver.Conn, table string) {
	t.Helper()
	//: the schema mirrors the operator-provided table documented in CLAUDE.md.
	ddl := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (ts DateTime, level String, message String) ENGINE = MergeTree ORDER BY ts`,
		table,
	)
	if err := conn.Exec(t.Context(), ddl); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
}

func TestIntegration_clickhouseFactory_roundtrip(t *testing.T) {
	//: container startup is heavy — keep this sequential (no t.Parallel()).
	f := startClickHouse(t)
	const table = "it_logs"
	conn := rawClient(t, f)
	createTable(t, conn, table)

	//: Open via the registered factory — the exact path production consumers take.
	sink, err := writer.Open("clickhouse", writer.ClickHouseConfig{
		Address:     f.addr,
		Database:    f.database,
		Table:       table,
		Credentials: itCreds{user: f.user, password: f.password},
		MaxRows:     5,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := t.Context()
	//: truncated to seconds — the column is DateTime (no sub-second precision).
	base := time.Now().UTC().Truncate(time.Second)
	records := []corelogger.RecordEvent{
		{Time: base, Level: level.Info, Message: "hello from integration test"},
		{Time: base.Add(time.Second), Level: level.Warn, Message: "second record"},
		{Time: base.Add(2 * time.Second), Level: level.Error, Message: "third record"},
	}
	//: drive the production chain (gate → async ring → batcher).
	for _, r := range records {
		if _, werr := sink.Write(ctx, r, []byte(r.Message)); werr != nil {
			t.Fatalf("Write(%q): %v", r.Message, werr)
		}
	}

	//: Flush synchronously drains the async ring and delivers the pending batch.
	if ferr := sink.Flush(ctx); ferr != nil {
		t.Fatalf("Flush: %v", ferr)
	}
	//: Close drains the chain a final time and releases the connection pool.
	if cerr := sink.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	//: read the rows back over a raw client to prove the round-trip survived.
	rows, err := conn.Query(ctx, fmt.Sprintf("SELECT ts, level, message FROM %s ORDER BY ts", table))
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer func() {
		//: release the result set after scanning.
		if rerr := rows.Close(); rerr != nil {
			t.Errorf("rows.Close: %v", rerr)
		}
	}()

	type row struct {
		ts  time.Time
		lvl string
		msg string
	}
	var got []row
	for rows.Next() {
		var r row
		if serr := rows.Scan(&r.ts, &r.lvl, &r.msg); serr != nil {
			t.Fatalf("Scan: %v", serr)
		}
		got = append(got, r)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("rows.Err: %v", rerr)
	}

	//: every written record must land exactly once.
	if len(got) != len(records) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(records), got)
	}
	for i, want := range records {
		//: the message content must survive the INSERT/SELECT round-trip.
		if got[i].msg != want.Message {
			t.Errorf("row[%d] message=%q want %q", i, got[i].msg, want.Message)
		}
		//: the level renders through Level.String() into the String column.
		if got[i].lvl != want.Level.String() {
			t.Errorf("row[%d] level=%q want %q", i, got[i].lvl, want.Level.String())
		}
		//: the timestamp survives at second granularity (DateTime column).
		if !got[i].ts.Equal(want.Time) {
			t.Errorf("row[%d] ts=%v want %v", i, got[i].ts, want.Time)
		}
	}
}
