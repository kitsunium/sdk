//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — an io_uring batch bounded in time (LoopValue.BatchBudget,
// ADR 0206): a batch stops reaping once its budget ran out, the next one
// reaps what was left without waiting, and the answers of a long batch leave
// as soon as it ends.
package loop

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// budgetConns is how many connections the budget's bursts use.
	budgetConns = 400
	// budgetWork is how long the test handler spins per line: what an
	// application's request costs, so a burst's batch lasts.
	budgetWork = 100 * time.Microsecond
)

// TestURingBudget_reapShape pins reapShape: no budget, or reaps already
// small, keep CQBatch reapRounds times; a budget reaps the same share in
// reaps of budgetChunk.
func TestURingBudget_reapShape(t *testing.T) {
	for _, c := range []struct {
		budget       time.Duration
		cqBatch      int
		chunk, round int
	}{
		{0, 512, 512, reapRounds},
		{time.Millisecond, 512, budgetChunk, reapRounds * 512 / budgetChunk},
		{time.Millisecond, 8, 8, reapRounds},
	} {
		chunk, rounds := reapShape(&sizing{batchBudget: c.budget, cqBatch: c.cqBatch})
		if chunk != c.chunk || rounds != c.round {
			t.Errorf("reapShape(budget %v, cqBatch %d) = %d, %d; want %d, %d", c.budget, c.cqBatch, chunk, rounds, c.chunk, c.round)
		}
	}
}

// spin busy-waits d: a handler's own work, which a sleep would not be.
func spin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

// burst holds g's loop at its batch end, has every connection send one line,
// releases the loop and returns each echo's delay from the release, sorted,
// on the reader's clock: when its goroutine read it.
func burst(t testing.TB, hold *atomic.Bool, held, release chan struct{}, holder stdnet.Conn, conns []stdnet.Conn) []time.Duration {
	t.Helper()
	start := loadBurst(t, hold, held, release, holder, conns)
	lat := make([]time.Duration, len(conns))
	var wg sync.WaitGroup
	//: one reader a connection.
	for i, c := range conns {
		wg.Go(func() {
			want := fmt.Sprintf("q %04d\n", i)
			buf := make([]byte, len(want))
			_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
			//: the echo, exact.
			if _, err := io.ReadFull(c, buf); err != nil || string(buf) != want {
				t.Errorf("connection %d: %q, %v, want %q", i, buf, err, want)
			}
			lat[i] = time.Since(start)
		})
	}
	wg.Wait()
	slices.Sort(lat)
	return lat
}

// loadBurst holds the loop at its batch end, has every connection send one
// line while it is held, then releases it and reads the holder's echo. It
// returns the release's instant.
func loadBurst(t testing.TB, hold *atomic.Bool, held, release chan struct{}, holder stdnet.Conn, conns []stdnet.Conn) time.Time {
	t.Helper()
	hold.Store(true)
	//: the line that holds the loop.
	if _, err := holder.Write([]byte("hold\n")); err != nil {
		t.Fatal(err)
	}
	<-held
	//: the burst, queued behind the held batch.
	for i, c := range conns {
		//: one line a connection.
		if _, err := fmt.Fprintf(c, "q %04d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	release <- struct{}{}
	got := make([]byte, len("hold\n"))
	//: the holder's echo.
	if _, err := io.ReadFull(holder, got); err != nil {
		t.Fatal(err)
	}
	return start
}

// burstStamped is burst timed by the kernel: each echo's delay from the
// release to the instant it reached its socket (SO_TIMESTAMPNS), sorted,
// then the same on the reader's clock. The kernel's clock does not need the
// reader to run: under GOMAXPROCS=1 the readers share the one P with the
// loop and with each other, and read an echo long after the kernel holds it
// (#1596).
func burstStamped(t testing.TB, hold *atomic.Bool, held, release chan struct{}, holder stdnet.Conn, conns []stdnet.Conn) (kern, wall []time.Duration) {
	t.Helper()
	//: every connection stamps what it receives.
	for _, c := range conns {
		stampReceives(t, c)
	}
	start := loadBurst(t, hold, held, release, holder, conns)
	kern, wall = make([]time.Duration, len(conns)), make([]time.Duration, len(conns))
	var wg sync.WaitGroup
	//: one reader a connection.
	for i, c := range conns {
		wg.Go(func() {
			want := fmt.Sprintf("q %04d\n", i)
			_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
			buf, at, err := readStamped(c, len(want))
			read := time.Now()
			//: the echo, exact.
			if err != nil || string(buf) != want {
				t.Errorf("connection %d: %q, %v, want %q", i, buf, err, want)
				return
			}
			//: no echo leaves before the release, nor is read before it arrives.
			if at.Before(start) || at.After(read) {
				t.Errorf("connection %d: the kernel stamped its echo at %v, outside the release %v and the read %v", i, at, start, read)
			}
			kern[i], wall[i] = at.Sub(start), read.Sub(start)
		})
	}
	wg.Wait()
	slices.Sort(kern)
	slices.Sort(wall)
	return kern, wall
}

// stampReceives sets SO_TIMESTAMPNS on c: the kernel stamps every segment
// it receives on c with the realtime clock, read back by readStamped.
func stampReceives(t testing.TB, c stdnet.Conn) {
	t.Helper()
	rc, err := c.(*stdnet.TCPConn).SyscallConn()
	//: a TCP connection has a descriptor.
	if err != nil {
		t.Fatal(err)
	}
	var serr error
	cerr := rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TIMESTAMPNS, 1)
	})
	//: Linux stamps TCP receives since 4.13; io_uring needs more.
	if err := errors.Join(cerr, serr); err != nil {
		t.Fatalf("SO_TIMESTAMPNS: %v", err)
	}
}

// errNoStamp is readStamped's refusal of a read the kernel did not stamp.
var errNoStamp = errors.New("the kernel delivered no SCM_TIMESTAMPNS")

// readStamped reads n bytes from c and returns them with the kernel's
// receive stamp of the first segment read (SCM_TIMESTAMPNS). A read with no
// stamp is an error: stampReceives was not honoured.
func readStamped(c stdnet.Conn, n int) ([]byte, time.Time, error) {
	rc, err := c.(*stdnet.TCPConn).SyscallConn()
	//: a TCP connection has a descriptor.
	if err != nil {
		return nil, time.Time{}, err
	}
	buf, oob := make([]byte, n), make([]byte, syscall.CmsgSpace(16))
	var got int
	var at time.Time
	var rerr error
	//: until the n bytes, or a failure.
	for got < n && rerr == nil && err == nil {
		err = rc.Read(func(fd uintptr) bool {
			m, oobn, _, _, e := syscall.Recvmsg(int(fd), buf[got:], oob, 0)
			//: nothing yet: the poller waits.
			if e == syscall.EAGAIN {
				return false
			}
			//: a failure, or the end of the stream.
			if e != nil || m == 0 {
				rerr = cmp.Or(e, io.ErrUnexpectedEOF)
				return true
			}
			got += m
			//: the first segment's stamp.
			if at.IsZero() {
				at = receiveStamp(oob[:oobn])
			}
			return true
		})
	}
	//: the read failed.
	if err = cmp.Or(err, rerr); err != nil {
		return buf[:got], at, err
	}
	//: the kernel did not stamp what it delivered.
	if at.IsZero() {
		return buf, at, errNoStamp
	}
	return buf, at, nil
}

// receiveStamp is the SCM_TIMESTAMPNS of oob, the zero time if it carries
// none. The build tag's architectures are 64-bit little-endian: a timespec
// is two native int64.
func receiveStamp(oob []byte) time.Time {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	//: a truncated control buffer carries no stamp.
	if err != nil {
		return time.Time{}
	}
	//: the timestamp among the control messages.
	for _, m := range msgs {
		//: SCM_TIMESTAMPNS, a whole timespec.
		if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SCM_TIMESTAMPNS && len(m.Data) >= 16 {
			return time.Unix(int64(binary.NativeEndian.Uint64(m.Data)), int64(binary.NativeEndian.Uint64(m.Data[8:])))
		}
	}
	return time.Time{}
}

// budgetGroup serves lines that cost budgetWork each on a loop of rings
// rings with budget, the loop held at its batch end on demand.
func budgetGroup(t testing.TB, budget time.Duration, rings int) (*Group, *atomic.Bool, chan struct{}, chan struct{}, stdnet.Conn, []stdnet.Conn) {
	t.Helper()
	var hold atomic.Bool
	held, release := make(chan struct{}, 1), make(chan struct{})
	h := &lineH{}
	h.onData = func(corenet.LoopIO, corenet.LoopConnRef, []byte) { spin(budgetWork) }
	h.batchEnd = func(corenet.LoopIO) {
		if hold.CompareAndSwap(true, false) {
			held <- struct{}{}
			<-release
		}
	}
	bc := backendCase{"iouring", corenet.LoopBackendIOURing}
	g, addr := serve(t, bc, h, func(c *Config) {
		c.Value.Loops, c.Value.Rings = 1, rings
		c.Value.BatchBudget = corenet.DurationValue(budget)
		if rings > 1 {
			c.hooks.primaryIdle = true
		}
	})
	all := heard(t, addr, budgetConns+1)
	// Closed by a reset: no TIME_WAIT left on the ephemeral range for the
	// tests after it (resetClose).
	t.Cleanup(func() { resetClose(all) })
	for _, c := range all {
		_ = c.SetDeadline(time.Time{})
	}
	return g, &hold, held, release, all[budgetConns], all[:budgetConns]
}

// cutCount sums g's rings' batches the budget cut.
func cutCount(g *Group) uint64 {
	var n uint64
	for _, r := range g.State().RingStates {
		n += r.BatchesCut
	}
	return n
}

// TestURingBudget_cutsBatches serves a burst of budgetConns lines, each
// costing budgetWork, on one ring and on a secondary, with no budget and a
// 2 ms one: every echo is exact; with the budget batches are cut and
// counted, the first echoes reach the client's socket sooner than when the
// whole burst is one batch, and none waits for a tick for want of a wait.
// The echoes are timed by the kernel's receive stamp, not by the readers:
// the readers share the process's Ps with the loop, and under GOMAXPROCS=1
// read an echo the kernel holds only once the loop lets them run (#1596).
func TestURingBudget_cutsBatches(t *testing.T) {
	//: the backend under test.
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	//: two descriptors a connection, and the server's.
	if lim := descriptorLimit(); lim < 2*budgetConns+256 {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	type tc struct {
		rings  int
		budget time.Duration
	}
	runCase := func(t *testing.T, c tc) {
		first := map[time.Duration]time.Duration{}
		//: the whole burst one batch, then cut by the budget.
		for _, budget := range []time.Duration{0, c.budget} {
			g, hold, held, release, holder, conns := budgetGroup(t, budget, c.rings)
			kern, wall := burstStamped(t, hold, held, release, holder, conns)
			cut := cutCount(g)
			t.Logf("budget %v: first echo %v, median %v, last %v at the socket (read %v, %v, %v); %d batches cut",
				budget, kern[0], kern[len(kern)/2], kern[len(kern)-1], wall[0], wall[len(wall)/2], wall[len(wall)-1], cut)
			first[budget] = kern[0]
			//: no budget cuts nothing.
			if budget == 0 && cut != 0 {
				t.Fatalf("no budget, yet %d batches cut", cut)
			}
			//: a budget cuts the burst.
			if budget > 0 && cut == 0 {
				t.Fatalf("a burst of %d lines of %v each under a %v budget, no batch cut", budgetConns, budgetWork, budget)
			}
		}
		//: the cut batches' answers left first.
		if first[c.budget] >= first[0] {
			t.Fatalf("first echo at the socket %v with a budget, %v without: the budget did not answer sooner", first[c.budget], first[0])
		}
	}
	for _, c := range []tc{
		{rings: 1, budget: 2 * time.Millisecond},
		{rings: 2, budget: 2 * time.Millisecond},
	} {
		t.Run(fmt.Sprintf("rings=%d", c.rings), func(t *testing.T) { runCase(t, c) })
	}
}
