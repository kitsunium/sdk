// Package sql_test — the statement observer (#1122): what Observe tells, in
// which order, and what the two shipped observers make of it.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// fakeConnector is the scripted driver as a driver.Connector.
type fakeConnector struct{ d *fakeDriver }

// Connect opens a connection to the scripted server.
func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open("") }

// Driver is the scripted driver.
func (c fakeConnector) Driver() driver.Driver { return c.d }

// tape is an observer that writes down every Begin and every end, in order.
type tape struct {
	mu    sync.Mutex
	lines []string
}

// Begin writes the statement down, and its end when told.
func (t *tape) Begin(_ context.Context, kind coresql.StatementKind, query string) func(int64, error) {
	t.write(fmt.Sprintf("begin %s %s", kind, query))
	return func(rows int64, err error) { t.write(fmt.Sprintf("end %s %d %v", kind, rows, err)) }
}

// write appends a line.
func (t *tape) write(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
}

// read is the lines so far.
func (t *tape) read() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.lines)
}

// observedPool opens a pool over f, observed by obs.
func observedPool(t *testing.T, f *fakeDB, obs coresql.StatementObserver) *stdsql.DB {
	t.Helper()
	db := stdsql.OpenDB(svcsql.Observe(fakeConnector{d: &fakeDriver{db: f}}, obs))
	return closeOnCleanup(t, db)
}

// TestEveryStatementIsToldOnceInOrder is the first acceptance: each Exec,
// Query, QueryRow, prepared Exec and Query, and each BEGIN, COMMIT and
// ROLLBACK — the savepoints of a nested Transact included — calls Begin once
// and end once with the driver's error, in order.
func TestEveryStatementIsToldOnceInOrder(t *testing.T) {
	t.Parallel()
	f := threeRows(newFakeDB(), "SELECT n FROM t").failOn("UPDATE broken")
	obs := &tape{}
	tm, err := svcsql.NewTransactor(svcsql.Config{
		DB: observedPool(t, f, obs), Dialect: coresql.DialectPostgres, Pool: svcsql.PoolConfig{MaxOpen: 1},
	})
	if err != nil {
		t.Fatalf("NewTransactor: %v", err)
	}
	err = tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		if _, err := ex.ExecContext(ctx, "UPDATE t SET n = 1"); err != nil {
			return err
		}
		rows, err := ex.QueryContext(ctx, "SELECT n FROM t")
		if err != nil {
			return err
		}
		for _, err := range svcsql.Each(rows, scanInt) {
			if err != nil {
				return err
			}
		}
		var n int64
		if err := ex.QueryRowContext(ctx, "SELECT n FROM t").Scan(&n); err != nil {
			return err
		}
		stmt, err := ex.(coresql.Preparer).PrepareContext(ctx, "SELECT n FROM t")
		if err != nil {
			return err
		}
		defer stmt.Close()
		if _, err := stmt.ExecContext(ctx); err != nil {
			return err
		}
		nested := tm.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
			_, err := ex.ExecContext(ctx, "UPDATE broken")
			return err
		})
		if nested == nil {
			return errWork
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	want := []string{
		"begin begin BEGIN", "end begin 0 <nil>",
		"begin exec UPDATE t SET n = 1", "end exec 0 <nil>",
		"begin query SELECT n FROM t", "end query 3 <nil>",
		"begin query SELECT n FROM t", "end query 1 <nil>",
		"begin prepare SELECT n FROM t", "end prepare 0 <nil>",
		"begin exec SELECT n FROM t", "end exec 0 <nil>",
		"begin exec SAVEPOINT ktn_sp_1", "end exec 0 <nil>",
		"begin exec UPDATE broken", "end exec 0 scripted failure",
		"begin exec ROLLBACK TO SAVEPOINT ktn_sp_1", "end exec 0 <nil>",
		"begin commit COMMIT", "end commit 0 <nil>",
	}
	if got := obs.read(); !slices.Equal(got, want) {
		t.Fatalf("told\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	rollback := &tape{}
	tm, _ = svcsql.NewTransactor(svcsql.Config{
		DB: observedPool(t, newFakeDB(), rollback), Dialect: coresql.DialectPostgres, Pool: svcsql.PoolConfig{MaxOpen: 1},
	})
	_ = tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error { return errWork })
	if got := rollback.read(); !slices.Contains(got, "begin rollback ROLLBACK") || !slices.Contains(got, "end rollback 0 <nil>") {
		t.Fatalf("a rolled back transaction told %v, want its ROLLBACK", got)
	}
}

// TestANilObserverIsNoWrapperAndALongQueryIsBounded pins ADR 0031's nil and
// the bound on what an observer receives.
func TestANilObserverIsNoWrapperAndALongQueryIsBounded(t *testing.T) {
	t.Parallel()
	c := fakeConnector{d: &fakeDriver{db: newFakeDB()}}
	if got := svcsql.Observe(c, nil); got != driver.Connector(c) {
		t.Fatalf("Observe(c, nil) = %T, want c itself", got)
	}
	obs := &tape{}
	db := observedPool(t, newFakeDB(), obs)
	long := "SELECT '" + strings.Repeat("é", svcsql.MaxObservedQuery) + "'"
	if _, err := db.ExecContext(t.Context(), long); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	told := strings.TrimPrefix(obs.read()[0], "begin exec ")
	if len(told) > svcsql.MaxObservedQuery || !strings.HasPrefix(long, told) || !utf8Valid(told) {
		t.Fatalf("told %d bytes, want at most %d whole characters of the text", len(told), svcsql.MaxObservedQuery)
	}
}

// utf8Valid reports whether s ends on a whole character.
func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "?") == s }

// records is a Logger that keeps what it is told.
type records struct {
	mu   sync.Mutex
	logs []string
}

// Log keeps the message and its attributes, rendered.
func (r *records) Log(_ context.Context, lv level.Level, msg string, attrs ...corelogger.AttrValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := lv.String() + " " + msg
	for _, a := range attrs {
		switch a.Value.Kind() {
		case corelogger.KindString:
			line += fmt.Sprintf(" %s=%s", a.Key, a.Value.String())
		case corelogger.KindInt64:
			line += fmt.Sprintf(" %s=%d", a.Key, a.Value.Int64())
		case corelogger.KindDuration:
			line += fmt.Sprintf(" %s=%s", a.Key, a.Value.Duration())
		default:
			line += fmt.Sprintf(" %s=%v", a.Key, a.Value.Any())
		}
	}
	r.logs = append(r.logs, line)
}

// With is the logger itself.
func (r *records) With(...corelogger.AttrValue) corelogger.Logger { return r }

// WithGroup is the logger itself.
func (r *records) WithGroup(string) corelogger.Logger { return r }

// Enabled says every level is on.
func (r *records) Enabled(context.Context, level.Level) bool { return true }

// lines is what was logged.
func (r *records) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.logs)
}

// TestSlowQueriesLogsTheSlowOneWithoutItsArguments is the second
// acceptance: nothing under the threshold, one record over it carrying the
// SQL text, the duration and the row count — and never an argument, pinned by
// a canary the record must not contain.
func TestSlowQueriesLogsTheSlowOneWithoutItsArguments(t *testing.T) {
	t.Parallel()
	clk := manualClock()
	f := newFakeDB().on("SELECT n FROM t WHERE secret = $1", func(int) ([]string, [][]driver.Value, error) {
		clk.Advance(2 * time.Second)
		return []string{"n"}, [][]driver.Value{{int64(1)}, {int64(2)}}, nil
	})
	log := &records{}
	db := observedPool(t, f, svcsql.SlowQueries(time.Second, log, clk))
	if _, err := db.ExecContext(t.Context(), "UPDATE fast"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := log.lines(); len(got) != 0 {
		t.Fatalf("logged %v for a statement under the threshold", got)
	}
	const canary = "canary-7f3a-password"
	rows, err := db.QueryContext(t.Context(), "SELECT n FROM t WHERE secret = $1", canary)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, err := range svcsql.Each(rows, scanInt) {
		if err != nil {
			t.Fatalf("Each: %v", err)
		}
	}
	got := log.lines()
	if len(got) != 1 {
		t.Fatalf("logged %v, want one record", got)
	}
	for _, part := range []string{"WARN", "query=SELECT n FROM t WHERE secret = $1", "duration=2s", "rows=2"} {
		if !strings.Contains(got[0], part) {
			t.Fatalf("record %q lacks %q", got[0], part)
		}
	}
	if strings.Contains(got[0], canary) {
		t.Fatalf("record %q carries the argument", got[0])
	}
}

// spans is a Tracer that keeps the spans it started.
type spans struct {
	mu    sync.Mutex
	ended []*span
}

// Start starts a recording span.
func (s *spans) Start(ctx context.Context, name string, params coretrace.SpanParams) (context.Context, coretrace.Span) {
	sp := &span{name: name, kind: params.Kind, owner: s}
	for _, a := range params.Attrs {
		sp.attrs = append(sp.attrs, a.Key)
	}
	return ctx, sp
}

// span is one recorded span.
type span struct {
	name   string
	kind   coretrace.SpanKind
	attrs  []string
	status coretrace.StatusCode
	owner  *spans
}

// SpanContext is the zero identity.
func (s *span) SpanContext() coretrace.SpanContextValue { return coretrace.SpanContextValue{} }

// SetAttrs keeps the keys.
func (s *span) SetAttrs(attrs ...coreotel.AttrValue) {
	for _, a := range attrs {
		s.attrs = append(s.attrs, a.Key)
	}
}

// AddEvent records nothing.
func (s *span) AddEvent(string, ...coreotel.AttrValue) {}

// SetStatus keeps the code.
func (s *span) SetStatus(code coretrace.StatusCode, _ string) { s.status = code }

// End hands the span to its tracer.
func (s *span) End() {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.ended = append(s.owner.ended, s)
}

// TestTracedOpensOneClientSpanPerStatement pins the span's name, kind,
// attributes and status: an error status for a failed statement.
func TestTracedOpensOneClientSpanPerStatement(t *testing.T) {
	t.Parallel()
	tracer := &spans{}
	db := observedPool(t, newFakeDB().failOn("UPDATE broken"), svcsql.Traced(tracer))
	if _, err := db.ExecContext(t.Context(), "UPDATE fine"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE broken"); err == nil {
		t.Fatal("the scripted failure did not fail")
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if len(tracer.ended) != 2 {
		t.Fatalf("ended %d spans, want 2", len(tracer.ended))
	}
	for i, wantStatus := range []coretrace.StatusCode{coretrace.StatusUnset, coretrace.StatusError} {
		sp := tracer.ended[i]
		if sp.name != "sql exec" || sp.kind != coretrace.SpanKindClient || sp.status != wantStatus {
			t.Errorf("span %d = %q kind %v status %v, want \"sql exec\" client %v", i, sp.name, sp.kind, sp.status, wantStatus)
		}
		if !slices.Contains(sp.attrs, "db.query.text") || !slices.Contains(sp.attrs, "db.response.returned_rows") {
			t.Errorf("span %d attributes %v", i, sp.attrs)
		}
	}
}
