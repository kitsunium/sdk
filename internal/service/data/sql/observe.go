package sql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// MaxObservedQuery is how many bytes of a statement's SQL text a
// [coresql.StatementObserver] receives: a longer text is cut at the last
// whole UTF-8 character within it. A generated IN list or a bulk VALUES
// clause is no slower to log for being a megabyte long, and no more useful.
const MaxObservedQuery int = 1024

// The SQL texts the three transaction verbs are told with.
const (
	textBegin    string = "BEGIN"
	textCommit   string = "COMMIT"
	textRollback string = "ROLLBACK"
)

// Observe returns c with obs told about every statement the connections it
// opens send: each Exec, Query and Prepare on a connection, each Exec and
// Query of a statement it prepared, and each transaction's BEGIN, COMMIT and
// ROLLBACK — savepoints included, which travel as statements. A caller opens
// its pool over it and hands that pool to this package as it always did:
//
//	pool := stdsql.OpenDB(sql.Observe(connector, sql.SlowQueries(time.Second, log, clock.System)))
//
// The observer receives the SQL text as written, cut to [MaxObservedQuery]
// bytes, and never an argument or a row. A query's end is told when its rows
// close, with how many were read. A call the driver skips with
// driver.ErrSkip — database/sql then prepares the statement, and the
// prepared calls are told in turn — ends with that error; the two observers
// this package ships ignore it.
//
// The connections keep every optional interface database/sql reads — a
// ping, a session reset, a validity check, a driver's own argument checker —
// answering as the driver's own do, and declining as database/sql's
// defaults would where the driver has none. A nil obs is no wrapper: c is
// returned as it is (ADR 0031).
func Observe(c driver.Connector, obs coresql.StatementObserver) driver.Connector {
	//: no observer is no wrapper.
	if obs == nil {
		//: the caller's connector, as it is.
		return c
	}
	//: every connection it opens is observed.
	return &observedConnector{inner: c, obs: obs}
}

// bounded cuts query to MaxObservedQuery bytes, at the last whole UTF-8
// character within them.
func bounded(query string) string {
	//: short enough already.
	if len(query) <= MaxObservedQuery {
		//: as written.
		return query
	}
	cut := MaxObservedQuery
	//: back to the start of the character the bound falls inside.
	for cut > 0 && !utf8.RuneStart(query[cut]) {
		cut--
	}
	//: the whole characters before the bound.
	return query[:cut]
}

// observedConnector opens observed connections.
type observedConnector struct {
	// inner is the caller's connector.
	inner driver.Connector
	// obs is told about every statement.
	obs coresql.StatementObserver
}

// Connect opens a connection of the inner connector, observed.
func (c *observedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	//: no connection, nothing to observe.
	if err != nil {
		//: the driver's own error.
		return nil, err
	}
	//: the connection, each statement told.
	return &observedConn{inner: conn, obs: c.obs}, nil
}

// Driver is the inner connector's driver.
func (c *observedConnector) Driver() driver.Driver {
	//: database/sql reads it for nothing but identity.
	return c.inner.Driver()
}

// Close closes the inner connector when it is an io.Closer, as database/sql
// would on a DB.Close.
func (c *observedConnector) Close() error {
	//: a connector that holds resources of its own.
	if closer, ok := c.inner.(io.Closer); ok {
		//: its own verdict.
		return closer.Close()
	}
	//: nothing to release.
	return nil
}

// observedConn is one connection whose statements are told to an observer.
type observedConn struct {
	// inner is the driver's connection.
	inner driver.Conn
	// obs is told about every statement.
	obs coresql.StatementObserver
}

// Prepare compiles a statement, told as a prepare.
func (c *observedConn) Prepare(query string) (driver.Stmt, error) {
	//: the context-free form database/sql no longer calls on its own.
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext compiles a statement, told as a prepare; the statement it
// returns tells its own executions.
func (c *observedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	end := c.obs.Begin(ctx, coresql.StatementPrepare, bounded(query))
	var stmt driver.Stmt
	var err error
	//: the context form when the driver has one.
	if preparer, ok := c.inner.(driver.ConnPrepareContext); ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		stmt, err = c.inner.Prepare(query)
	}
	end(0, err)
	//: nothing compiled.
	if err != nil {
		//: the driver's own error.
		return nil, err
	}
	//: the statement, its executions told.
	return &observedStmt{inner: stmt, conn: c, query: bounded(query)}, nil
}

// Close closes the driver's connection.
func (c *observedConn) Close() error {
	//: its own verdict.
	return c.inner.Close()
}

// Begin opens a transaction; database/sql calls BeginTx.
func (c *observedConn) Begin() (driver.Tx, error) {
	//: the default options.
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx opens a transaction, told as BEGIN; its end is told as COMMIT or
// ROLLBACK.
func (c *observedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	end := c.obs.Begin(ctx, coresql.StatementBegin, textBegin)
	var tx driver.Tx
	var err error
	//: the options are honoured only by a driver that takes them.
	if beginner, ok := c.inner.(driver.ConnBeginTx); ok {
		tx, err = beginner.BeginTx(ctx, opts)
	} else {
		//: database/sql refuses non-default options itself for such a driver.
		tx, err = c.inner.Begin() //nolint:staticcheck // the driver offers no other form
	}
	end(0, err)
	//: no transaction.
	if err != nil {
		//: the driver's own error.
		return nil, err
	}
	//: its end, told on the context it began under: an observer reads its
	//: values — a trace, a request — and never waits on it.
	return &observedTx{inner: tx, obs: c.obs, ctx: ctx}, nil
}

// ExecContext runs a statement that returns no rows, told as an exec. A
// driver without the context form answers ErrSkip, and database/sql prepares
// the statement instead.
func (c *observedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.inner.(driver.ExecerContext)
	//: database/sql falls back to Prepare, which is told.
	if !ok {
		//: skipped, before anything is told.
		return nil, driver.ErrSkip
	}
	end := c.obs.Begin(ctx, coresql.StatementExec, bounded(query))
	result, err := execer.ExecContext(ctx, query, args)
	end(affected(result, err), err)
	//: the driver's own answer.
	return result, err
}

// QueryContext runs a statement that returns rows, told as a query whose end
// is the rows' close.
func (c *observedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.inner.(driver.QueryerContext)
	//: database/sql falls back to Prepare, which is told.
	if !ok {
		//: skipped, before anything is told.
		return nil, driver.ErrSkip
	}
	end := c.obs.Begin(ctx, coresql.StatementQuery, bounded(query))
	rows, err := queryer.QueryContext(ctx, query, args)
	//: no rows to count.
	if err != nil {
		end(0, err)
		//: the driver's own error.
		return nil, err
	}
	//: told when the rows close.
	return &observedRows{inner: rows, end: end}, nil
}

// Ping forwards to the driver's own, and answers nil for a driver without
// one, as database/sql does.
func (c *observedConn) Ping(ctx context.Context) error {
	//: the driver's own probe.
	if pinger, ok := c.inner.(driver.Pinger); ok {
		//: its verdict.
		return pinger.Ping(ctx)
	}
	//: database/sql treats a connection without one as alive.
	return nil
}

// ResetSession forwards to the driver's own, and answers nil for a driver
// without one, as database/sql does.
func (c *observedConn) ResetSession(ctx context.Context) error {
	//: the driver's own reset.
	if resetter, ok := c.inner.(driver.SessionResetter); ok {
		//: its verdict.
		return resetter.ResetSession(ctx)
	}
	//: nothing to reset.
	return nil
}

// IsValid forwards to the driver's own, and answers true for a driver
// without one, as database/sql does.
func (c *observedConn) IsValid() bool {
	//: the driver's own check.
	if validator, ok := c.inner.(driver.Validator); ok {
		//: its verdict.
		return validator.IsValid()
	}
	//: database/sql reuses such a connection.
	return true
}

// CheckNamedValue forwards to the driver's own argument checker, and answers
// ErrSkip for a driver without one, which hands the argument to
// database/sql's default conversion — what it would have done.
func (c *observedConn) CheckNamedValue(value *driver.NamedValue) error {
	//: the driver's own conversion.
	if checker, ok := c.inner.(driver.NamedValueChecker); ok {
		//: its verdict.
		return checker.CheckNamedValue(value)
	}
	//: database/sql's default conversion.
	return driver.ErrSkip
}

// observedStmt is a prepared statement whose executions are told.
type observedStmt struct {
	// inner is the driver's statement.
	inner driver.Stmt
	// conn is the observed connection that prepared it.
	conn *observedConn
	// query is the bounded text it was prepared from.
	query string
}

// Close closes the driver's statement.
func (s *observedStmt) Close() error {
	//: its own verdict.
	return s.inner.Close()
}

// NumInput is the driver's own count.
func (s *observedStmt) NumInput() int {
	//: -1 when the driver does not know.
	return s.inner.NumInput()
}

// Exec runs the statement; database/sql calls ExecContext.
func (s *observedStmt) Exec(args []driver.Value) (driver.Result, error) {
	//: the context form, with the positional arguments named by position.
	return s.ExecContext(context.Background(), named(args))
}

// Query runs the statement; database/sql calls QueryContext.
func (s *observedStmt) Query(args []driver.Value) (driver.Rows, error) {
	//: the context form, with the positional arguments named by position.
	return s.QueryContext(context.Background(), named(args))
}

// ExecContext runs the statement, told as an exec.
func (s *observedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	end := s.conn.obs.Begin(ctx, coresql.StatementExec, s.query)
	var result driver.Result
	var err error
	//: the context form when the driver has one.
	if execer, ok := s.inner.(driver.StmtExecContext); ok {
		result, err = execer.ExecContext(ctx, args)
	} else {
		result, err = s.inner.Exec(values(args)) //nolint:staticcheck // the driver offers no other form
	}
	end(affected(result, err), err)
	//: the driver's own answer.
	return result, err
}

// QueryContext runs the statement, told as a query whose end is the rows'
// close.
func (s *observedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	end := s.conn.obs.Begin(ctx, coresql.StatementQuery, s.query)
	var rows driver.Rows
	var err error
	//: the context form when the driver has one.
	if queryer, ok := s.inner.(driver.StmtQueryContext); ok {
		rows, err = queryer.QueryContext(ctx, args)
	} else {
		rows, err = s.inner.Query(values(args)) //nolint:staticcheck // the driver offers no other form
	}
	//: no rows to count.
	if err != nil {
		end(0, err)
		//: the driver's own error.
		return nil, err
	}
	//: told when the rows close.
	return &observedRows{inner: rows, end: end}, nil
}

// CheckNamedValue forwards to the statement's own argument checker, else the
// connection's — the order database/sql reads them in — else answers ErrSkip.
func (s *observedStmt) CheckNamedValue(value *driver.NamedValue) error {
	//: the statement's own conversion.
	if checker, ok := s.inner.(driver.NamedValueChecker); ok {
		//: its verdict.
		return checker.CheckNamedValue(value)
	}
	//: the connection's, then database/sql's default.
	return s.conn.CheckNamedValue(value)
}

// observedTx is a transaction whose end is told.
type observedTx struct {
	// inner is the driver's transaction.
	inner driver.Tx
	// obs is told about its end.
	obs coresql.StatementObserver
	// ctx is the context it began under.
	ctx context.Context
}

// Commit commits, told as COMMIT.
func (t *observedTx) Commit() error {
	end := t.obs.Begin(t.ctx, coresql.StatementCommit, textCommit)
	err := t.inner.Commit()
	end(0, err)
	//: the driver's own verdict.
	return err
}

// Rollback rolls back, told as ROLLBACK.
func (t *observedTx) Rollback() error {
	end := t.obs.Begin(t.ctx, coresql.StatementRollback, textRollback)
	err := t.inner.Rollback()
	end(0, err)
	//: the driver's own verdict.
	return err
}

// observedRows counts the rows read and tells the query's end at the close.
type observedRows struct {
	// inner is the driver's rows.
	inner driver.Rows
	// end is told once, at the first close.
	end func(rows int64, err error)
	// read counts the rows Next delivered.
	read int64
	// failed is the first failure Next met, other than the end.
	failed error
	// told is set once end was told.
	told bool
}

// Columns names the result columns.
func (r *observedRows) Columns() []string {
	//: the driver's own.
	return r.inner.Columns()
}

// Next reads a row, counted.
func (r *observedRows) Next(dest []driver.Value) error {
	err := r.inner.Next(dest)
	//: a row read.
	if err == nil {
		r.read++
		//: delivered.
		return nil
	}
	//: the end of the set is not a failure.
	if !errors.Is(err, io.EOF) && r.failed == nil {
		r.failed = err
	}
	//: the driver's own answer.
	return err
}

// Close closes the rows and tells the query's end, once.
func (r *observedRows) Close() error {
	err := r.inner.Close()
	//: database/sql closes at most once, but a driver's double may not.
	if !r.told {
		r.told = true
		r.end(r.read, errors.Join(r.failed, err))
	}
	//: the driver's own verdict.
	return err
}

// affected is an exec's rows affected when the driver reports them, else 0.
func affected(result driver.Result, err error) int64 {
	//: a failed exec affected nothing it can say.
	if err != nil || result == nil {
		//: none.
		return 0
	}
	n, countErr := result.RowsAffected()
	//: a driver that cannot count.
	if countErr != nil {
		//: none said.
		return 0
	}
	//: the driver's count.
	return n
}

// named turns positional arguments into named ones, by position.
func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	//: ordinals count from 1.
	for i, arg := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	//: the context form's arguments.
	return out
}

// values turns named arguments into positional ones.
func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	//: the order database/sql bound them in.
	for i, arg := range args {
		out[i] = arg.Value
	}
	//: the legacy form's arguments.
	return out
}

// SlowQueries returns an observer that logs, at Warn, each statement that
// took threshold or longer, timed on clk: its kind, its SQL text, its
// duration, the rows it read or affected, and — when it failed — the driver
// error's class as [Withheld] renders it, never its text. A statement under
// the threshold logs nothing, and costs one reading of the clock at each
// end. A non-positive threshold logs every statement; a nil clk is
// clock.System.
func SlowQueries(threshold time.Duration, log corelogger.Logger, clk clock.Clock) coresql.StatementObserver {
	//: the wall clock is the only non-arbitrary default.
	if clk == nil {
		clk = clock.System
	}
	//: the threshold, the logger and the clock.
	return slowQueries{threshold: threshold, log: log, clk: clk}
}

// slowQueries is the observer SlowQueries returns.
type slowQueries struct {
	// threshold is the duration from which a statement is logged.
	threshold time.Duration
	// log receives the records.
	log corelogger.Logger
	// clk times each statement.
	clk clock.Clock
}

// Begin starts timing a statement; its end logs it when it was slow.
func (s slowQueries) Begin(ctx context.Context, kind coresql.StatementKind, query string) func(rows int64, err error) {
	start := s.clk.Now()
	//: told once, when the statement ended.
	return func(rows int64, err error) {
		took := s.clk.Since(start)
		//: fast enough, a skipped call, or a level that is off.
		if took < s.threshold || errors.Is(err, driver.ErrSkip) || s.log == nil || !s.log.Enabled(ctx, level.Warn) {
			//: nothing to say.
			return
		}
		attrs := []corelogger.AttrValue{
			{Key: "kind", Value: corelogger.StringValue(kind.String())},
			{Key: "query", Value: corelogger.StringValue(query)},
			{Key: "duration", Value: corelogger.DurationValue(took)},
			{Key: "rows", Value: corelogger.Int64Value(rows)},
		}
		//: the class of failure, never the driver's words.
		if err != nil {
			attrs = append(attrs, corelogger.AttrValue{Key: "error", Value: corelogger.StringValue(NewWithheld(err).Error())})
		}
		s.log.Log(ctx, level.Warn, "sql: a slow statement", attrs...)
	}
}

// Traced returns an observer that opens one client span per statement under
// the span ctx carries, named "sql " and the kind, with the SQL text as
// db.query.text, and ends it when the statement ends, with the rows as
// db.response.returned_rows and an error status — the driver error's class
// as [Withheld] renders it — when it failed.
func Traced(tracer coretrace.Tracer) coresql.StatementObserver {
	//: the tracer the spans start on.
	return traced{tracer: tracer}
}

// traced is the observer Traced returns.
type traced struct {
	// tracer starts the spans.
	tracer coretrace.Tracer
}

// Begin opens the statement's span; its end closes it.
func (t traced) Begin(ctx context.Context, kind coresql.StatementKind, query string) func(rows int64, err error) {
	_, span := t.tracer.Start(ctx, "sql "+kind.String(), coretrace.SpanParams{
		Kind:  coretrace.SpanKindClient,
		Attrs: []coreotel.AttrValue{coreotel.String("db.query.text", query)},
	})
	//: told once, when the statement ended.
	return func(rows int64, err error) {
		span.SetAttrs(coreotel.Int64("db.response.returned_rows", rows))
		//: a failure's class, never the driver's words; a skipped call is none.
		if err != nil && !errors.Is(err, driver.ErrSkip) {
			span.SetStatus(coretrace.StatusError, NewWithheld(err).Error())
		}
		span.End()
	}
}
