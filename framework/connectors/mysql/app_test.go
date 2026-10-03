package mysql_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The product the end-to-end tests run: one service, whose store the
// default database keeps — on MySQL.

type entry struct {
	ID string `json:"id"`
}

// migrations are the product's: one DDL statement each — on MySQL a DDL
// statement commits by itself. Their tables have no IF NOT EXISTS: running
// one twice fails.
var migrations = []sql.Migration{
	{Version: 20260901120000, Name: "create entries", Up: sql.Statements("CREATE TABLE entries (id VARCHAR(64) PRIMARY KEY)"), Down: sql.Statements("DROP TABLE entries")},
	{Version: 20260902120000, Name: "create trail", Up: sql.Statements("CREATE TABLE trail (id VARCHAR(64) PRIMARY KEY)"), Down: sql.Statements("DROP TABLE trail")},
}

// ledger is the product in production, its data in a directory of the
// test's; its URL is the variable LEDGER_DATABASE_URL, which the caller
// sets.
func ledger(t *testing.T, logs io.Writer, opts ...kit.DatabaseOption) *kit.App {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	books := kit.NewService("books", "The books.")
	books.Store("entries", func(e entry) string { return e.ID })
	return kit.NewApp("ledger", books).With(
		kit.Database("database", mysql.Engine(), opts...),
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Logs(logs),
	)
}

// lifecycle is what run needs of an app.
type lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// run starts app and stops it with the test.
func run(t *testing.T, app lifecycle) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Logf("stop: %v", err)
		}
	})
}

// status is the status of GET path on the app at base.
func status(t *testing.T, base, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Logf("closing the body: %v", err)
	}
	return resp.StatusCode
}

// eventually waits until the app is ready, or fails the test.
func eventually(t *testing.T, base, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for status(t, base, "/_kit/health/ready") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatalf("not ready %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// databaseOf is the runtime's database in the graph g.
func databaseOf(t *testing.T, g *model.Graph) model.Database {
	t.Helper()
	if g.Runtime == nil || len(g.Runtime.Databases) != 1 {
		t.Fatalf("runtime %+v", g.Runtime)
	}
	return g.Runtime.Databases[0]
}

// syncBuffer collects logs written from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// proxy stands between the product and the server, so a test can take the
// server away and bring it back.
type proxy struct {
	target string
	addr   string

	mu    sync.Mutex
	ln    net.Listener
	conns []net.Conn
}

// newProxy listens on a loopback port and forwards to target.
//
// Goroutine lifecycle: serve runs until the listener closes, which down does
// at the test's cleanup.
func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	p := &proxy{target: target}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.ln, p.addr = ln, ln.Addr().String()
	go p.serve(ln)
	t.Cleanup(p.down)
	return p
}

// serve forwards each connection ln accepts to the target.
//
// Goroutine lifecycle: two pipes per connection, each ending when either side
// closes — down closes both.
func (p *proxy) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s, err := net.Dial("tcp", p.target)
		if err != nil {
			closeQuietly(c)
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, s)
		p.mu.Unlock()
		go pipe(s, c)
		go pipe(c, s)
	}
}

// down closes the listener and every connection through it.
func (p *proxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		closeQuietly(p.ln)
		p.ln = nil
	}
	for _, c := range p.conns {
		closeQuietly(c)
	}
	p.conns = nil
}

// up listens again on the same address.
//
// Goroutine lifecycle: serve runs until the listener closes, which down does.
func (p *proxy) up(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.serve(ln)
}

// pipe copies src to dst, then closes dst. A proxy cut in the middle of a
// copy is what the tests do on purpose: the error that brings is expected.
func pipe(dst, src net.Conn) {
	if _, err := io.Copy(dst, src); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("proxy: %v", err)
	}
	closeQuietly(dst)
}

// closeQuietly closes c, logging an error other than an already closed one.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("proxy: closing: %v", err)
	}
}
