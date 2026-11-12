package consensus

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// transportWallClock is package time's surface the transport must take from
// TransportConfig.Clock instead (#948): every wait, and every reading an age,
// a backoff or an expiry is measured against. time.Time, time.Duration and
// their methods are values, not readings, and stay free.
var transportWallClock = []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}

// deadlineSetters are the net.Conn methods that take an absolute wall time:
// the kernel arms them, so the one reading the transport keeps from the wall
// clock is the argument of one of them, written time.Now().Add(d), d being
// what the injected clock left.
var deadlineSetters = []string{"SetDeadline", "SetReadDeadline", "SetWriteDeadline"}

// isTimeCall reports whether e is a call of package time's function name.
func isTimeCall(e ast.Expr, name string) (*ast.SelectorExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return sel, ok && pkg.Name == "time"
}

// deadlineReadings returns the positions of the time.Now selectors written
// as conn.SetDeadline(time.Now().Add(d)), or one of its siblings: the only
// wall-clock readings the audit allows.
func deadlineReadings(file *ast.File) map[token.Pos]bool {
	allowed := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		set, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !slices.Contains(deadlineSetters, set.Sel.Name) {
			return true
		}
		add, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		addSel, ok := add.Fun.(*ast.SelectorExpr)
		if !ok || addSel.Sel.Name != "Add" {
			return true
		}
		if now, ok := isTimeCall(addSel.X, "Now"); ok {
			allowed[now.Pos()] = true
		}
		return true
	})
	return allowed
}

// transportWallClockCalls returns every reading of, or wait on, the wall
// clock in one file, but the deadline readings.
func transportWallClockCalls(tb testing.TB, path string) []string {
	tb.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	//: a file this audit cannot parse is a blind spot, not a pass.
	if err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
	allowed := deadlineReadings(file)
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "time" || !slices.Contains(transportWallClock, sel.Sel.Name) || allowed[sel.Pos()] {
			return true
		}
		found = append(found, fset.Position(sel.Pos()).String()+": time."+sel.Sel.Name)
		return true
	})
	return found
}

// TestTransportNeverWaitsOnTheWallClock holds the transport's sources —
// tcp*.go, its tests aside — to the injected clock (#948), as the
// scheduler's TestPackageNeverWaitsOnTheWallClock holds its domain: a link's
// age, the redial backoff, the peer certificate's end and the accept backoff
// are read on TransportConfig.Clock, so a ManualClock drives them, and the
// only wall-clock readings left are the deadlines the kernel arms. It reads
// the files of the package directory, which Bazel ships as the test's data.
func TestTransportNeverWaitsOnTheWallClock(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "tcp") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		for _, call := range transportWallClockCalls(t, name) {
			t.Errorf("%s — the transport reads its clock through TransportConfig.Clock, never through package time", call)
		}
	}
	//: an audit that inspected nothing proves nothing.
	if scanned == 0 {
		t.Fatal("no transport source found: the audit inspected nothing")
	}
}

// TestTransportWallClockAuditCatchesAReading shows the audit above fails on
// what it claims to catch, and lets through only the deadline form.
func TestTransportWallClockAuditCatchesAReading(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		want       int
	}{
		{"an injected time.Since is caught", `func f(t0 time.Time) bool { return time.Since(t0) > time.Hour }`, 1},
		{"a reading kept for later is caught", `func f(c net.Conn) error { d := time.Now().Add(time.Second); return c.SetDeadline(d) }`, 1},
		{"every wait is caught", `func f() { time.Sleep(1); <-time.After(1); _ = time.NewTimer(1); _ = time.NewTicker(1); _ = time.AfterFunc(1, nil); _ = time.Tick(1); _ = time.Until(time.Time{}) }`, 7},
		{"a deadline the kernel arms is allowed", `func f(c net.Conn) error { _ = c.SetReadDeadline(time.Now().Add(1)); _ = c.SetWriteDeadline(time.Now().Add(1)); return c.SetDeadline(time.Now().Add(1)) }`, 0},
		{"an injected clock is allowed", `func f(c interface{ Now() time.Time }) time.Time { return c.Now().Add(time.Second) }`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "fixture.go")
			src := "package fixture\n\nimport (\n\t\"net\"\n\t\"time\"\n)\n\nvar _ net.Conn\n\n" + tc.body + "\n"
			if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := transportWallClockCalls(t, path); len(got) != tc.want {
				t.Errorf("found %d wall-clock readings (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestALinkPastMaxConnectionAgeIsRetiredOnTheClock ages a sender's link by
// advancing the transport's ManualClock, with no sleep (#948): at exactly
// MaxConnectionAge the link still carries the batch, one nanosecond past it
// the link is closed and the batch goes out on a fresh dial.
func TestALinkPastMaxConnectionAgeIsRetiredOnTheClock(t *testing.T) {
	t.Parallel()
	const age = 10 * time.Minute
	clk := clock.NewManualClock(time.Now())
	p := newLinkPairWith(t, func(c *TransportConfig) { c.Clock, c.MaxConnectionAge = clk, age })
	k := outKey{p.b.id, laneControl}
	var link senderLink
	defer link.close()
	send := func(term uint64) {
		t.Helper()
		p.a.tr.sendBatch(k, &link, []outFrame{{head: p.frame(term)}})
		if !p.delivered(2 * time.Second) {
			t.Fatalf("the frame of term %d never arrived", term)
		}
	}
	send(2)
	first := link.conn
	if first == nil {
		t.Fatal("setup: the first batch dialled no link")
	}
	clk.Advance(age)
	send(3)
	if link.conn != first {
		t.Fatal("a link exactly MaxConnectionAge old was retired")
	}
	clk.Advance(time.Nanosecond)
	send(4)
	if link.conn == nil || link.conn == first {
		t.Fatal("a link past MaxConnectionAge on the transport's clock was not retired")
	}
	if _, err := first.Write([]byte{0}); err == nil {
		t.Fatal("the retired link's connection still takes writes")
	}
}

// TestARedialBacksOffOnTheClock holds the redial backoff on the transport's
// ManualClock (#948): after a failed dial no dial is tried, the peer back
// up, until the clock moves redialBackoff on.
func TestARedialBacksOffOnTheClock(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Now())
	p := newLinkPairWith(t, func(c *TransportConfig) { c.Clock = clk })
	k := outKey{p.b.id, laneControl}
	var link senderLink
	defer link.close()
	batch := []outFrame{{head: p.frame(2)}}
	abandon(p.b.tr.Close())
	p.a.tr.sendBatch(k, &link, batch)
	if link.conn != nil {
		t.Fatal("setup: a dial to a closed peer succeeded")
	}
	p.b.listen(t, p.ca)
	p.bindB(t)
	p.b.tr.SetMembership(1, p.members)
	clk.Advance(redialBackoff - time.Nanosecond)
	p.a.tr.sendBatch(k, &link, batch)
	if link.conn != nil {
		t.Fatal("the sender dialled again before redialBackoff passed on its clock")
	}
	clk.Advance(time.Nanosecond)
	p.a.tr.sendBatch(k, &link, batch)
	if link.conn == nil {
		t.Fatal("the sender did not dial again once redialBackoff passed on its clock")
	}
	if !p.delivered(2 * time.Second) {
		t.Fatal("the batch written after the backoff never arrived")
	}
}
