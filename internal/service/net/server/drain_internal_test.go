// Package server — the drain (plan P6): readiness first, the grace, then the
// listeners, under continuous arrivals; and LoopsAuto's snapshot in State.
package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// drainGraceTest is the grace the drain test gives, and drainLagTest how
// long its clients keep dialling once readiness turned false: a load
// balancer's lag, shorter than the grace.
const (
	drainGraceTest = 400 * time.Millisecond
	drainLagTest   = 150 * time.Millisecond
)

// TestDrain_graceUnderArrivals is the drain under continuous arrivals, on
// every backend the platform has. Clients dial new connections back to back,
// each one line and its echo; they stop once readiness has been false for
// drainLagTest, as a load balancer does. Kept connections, heard once, wait
// for the server's end. No dial may fail, no echo be cut short, no kept
// connection be reset: the grace serves what arrives after readiness turned
// false, and the drain closes nothing before it read it.
func TestDrain_graceUnderArrivals(t *testing.T) {
	for _, p := range drainProtos() {
		for _, b := range loopBackends() {
			t.Run(p.name+"/"+b.String(), func(t *testing.T) { drainGraceUnderArrivals(t, p, b) })
		}
	}
}

// drainGraceUnderArrivals is TestDrain_graceUnderArrivals on protocol p and
// backend b.
func drainGraceUnderArrivals(t *testing.T, p drainProto, b corenet.LoopBackend) {
	srv := New(WithDrainGrace(drainGraceTest))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	counts := p.group(srv, b)
	procs := runtime.GOMAXPROCS(0)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := srv.State()
	if ls := st.Loops[0]; ls.Loops != max(procs-1, 1) || ls.LoopsSource != "auto" {
		t.Fatalf("LoopsAuto: loops %d (%q), want %d (%q)", ls.Loops, ls.LoopsSource, max(procs-1, 1), "auto")
	}
	addr := st.Listeners[0].Address

	kept := make([]stdnet.Conn, 8)
	for i := range kept {
		kept[i] = p.keep(t, addr)
	}

	var stop, notReady atomic.Bool
	var late atomic.Int64
	errc := make(chan string, 64)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				after := notReady.Load()
				t0 := time.Now()
				if _, msg, _ := p.exchange(addr); msg != "" {
					msg += " began " + t0.Format("15:04:05.000")
					select {
					case errc <- msg + ", ended " + time.Now().Format("15:04:05.000") + ", readiness false " + strconv.FormatBool(notReady.Load()):
					default:
					}
					return
				}
				if after {
					late.Add(1)
				}
				// Paced, so the clients' closed ports in TIME_WAIT never run
				// the ephemeral range out.
				time.Sleep(2 * time.Millisecond)
			}
		})
	}
	wg.Go(func() {
		// The load balancer: it routes on readiness, with a lag.
		for srv.readiness(context.Background()) == nil {
			time.Sleep(5 * time.Millisecond)
		}
		notReady.Store(true)
		time.Sleep(drainLagTest)
		stop.Store(true)
	})
	for _, c := range kept {
		wg.Go(func() {
			if msg := p.end(c); msg != "" {
				select {
				case errc <- "kept: " + msg:
				default:
				}
			}
		})
	}

	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	began := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	took := time.Since(began)
	wg.Wait()
	close(errc)
	for msg := range errc {
		t.Errorf("cut short: %s; Shutdown began %s", msg, began.Format("15:04:05.000"))
	}
	if took < drainGraceTest {
		t.Fatalf("Shutdown took %v, under the grace %v", took, drainGraceTest)
	}
	if late.Load() == 0 {
		t.Fatal("no connection was served once readiness turned false: the grace served nothing")
	}
	if o, cl := counts(); o != cl {
		t.Fatalf("%d opened, %d closed", o, cl)
	}
}

// drainCloseWindow is how close to the listener's close a connection's dial
// may have returned, and the connection still be reset: one the kernel completed after the
// take's last empty read, or whose handshake it had not finished, meets the
// close (ADR 0196 §D4). Nothing else may be.
const drainCloseWindow = 20 * time.Millisecond

// drainProbeDial bounds each dial of the probe that dates the listener's
// close: a SYN the kernel drops instead of refusing is abandoned and dialled
// again, so the refusal that dates the close is never one that waited for a
// retransmission.
const drainProbeDial = 5 * time.Millisecond

// TestDrain_arrivalsAcrossTheDrain keeps arrivals going through the whole
// drain, on every backend the platform has: clients dial back to back and
// stop only once a dial fails — the listener closed. Every connection whose
// dial succeeded must read its echo, never a reset nor an end before it,
// unless its dial returned within drainCloseWindow of the listener's close:
// the close's own window, which no sequence of calls on one listener closes.
// The close is dated by the return of a refused dial, which follows it — the
// kernel refuses a SYN only once no listener is left on the port —, so a
// connection is judged against a date the close cannot be later than.
// Each backend takes its listen queue before it closes the listener, so
// what waited there is served.
func TestDrain_arrivalsAcrossTheDrain(t *testing.T) {
	for _, p := range drainProtos() {
		for _, b := range loopBackends() {
			t.Run(p.name+"/"+b.String(), func(t *testing.T) { drainArrivalsAcross(t, p, b) })
		}
	}
}

// drainArrivalsAcross is TestDrain_arrivalsAcrossTheDrain on protocol p and
// backend b.
func drainArrivalsAcross(t *testing.T, p drainProto, b corenet.LoopBackend) {
	if runtime.GOOS == "windows" {
		// ADR 0196 §D3: a Windows listener cannot be duplicated into the
		// portable backend's own, so its drain takes no queue and a close
		// under arrivals resets what waits; the grace test above holds.
		t.Skip("the portable backend takes no listen queue on Windows")
	}
	srv := New(WithDrainGrace(50 * time.Millisecond))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	counts := p.group(srv, b)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := srv.State().Listeners[0].Address
	type cut struct {
		at, failed time.Time
		msg        string
	}
	var draining atomic.Bool
	var during, refused atomic.Int64
	var mu sync.Mutex
	var cuts []cut
	// accepted is when the last dial the listener accepted began: the close
	// came after it. closed is the earliest return of a refused dial: the
	// close came before it.
	var accepted, closed time.Time
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				began := draining.Load()
				//: a connection is placed by when the kernel completed it:
				//: an h2c exchange on a slow guest (NetBSD) began 20.036 ms
				//: before the close and was completed inside the window.
				start := time.Now()
				at, msg, derr := p.exchange(addr)
				failed := time.Now()
				mu.Lock()
				if derr == nil && start.After(accepted) {
					accepted = start
				}
				switch {
				case derr != nil && !began && !draining.Load():
					cuts = append(cuts, cut{at, failed, "refused before the drain: " + msg})
				case derr != nil:
					refused.Add(1)
					if errors.Is(derr, syscall.ECONNREFUSED) && (closed.IsZero() || failed.Before(closed)) {
						closed = failed
					}
				case msg != "":
					cuts = append(cuts, cut{at, failed, msg})
				case began:
					during.Add(1)
				}
				mu.Unlock()
				//: back to back, so arrivals reach the listener until it
				//: closes: a pause between two exchanges left it idle for the
				//: pause, which NetBSD's 10 ms clock makes 20 ms (#1385).
				if derr != nil {
					return
				}
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	draining.Store(true)
	var probe drainDate
	wg.Go(func() {
		probe = drainProbe(addr, time.Now().Add(10*time.Second))
	})
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	wg.Wait()
	if during.Load() == 0 || refused.Load() == 0 {
		t.Fatalf("arrivals did not cross the drain: %d served during it, %d refused after", during.Load(), refused.Load())
	}
	if probe.refused.IsZero() {
		t.Fatalf("the listener's close was never dated: no dial was refused within 10s of the drain")
	}
	if probe.accepted.After(accepted) {
		accepted = probe.accepted
	}
	if closed.IsZero() || probe.refused.Before(closed) {
		closed = probe.refused
	}
	window := 0
	for _, c := range cuts {
		if c.at.After(closed.Add(-drainCloseWindow)) {
			window++
			continue
		}
		t.Errorf("cut short %v before the listener closed, failed %v before it; the last dial the listener accepted began %v after this one returned: %s", closed.Sub(c.at), closed.Sub(c.failed), accepted.Sub(c.at), c.msg)
	}
	t.Logf("%d served during the drain; %d reset within %v of the close; the close dated %v after the last dial accepted began, the probe's refusal taking %v", during.Load(), window, drainCloseWindow, closed.Sub(accepted), probe.refused.Sub(probe.began))
	if o, cl := counts(); o != cl {
		t.Fatalf("%d opened, %d closed", o, cl)
	}
}

// drainDate is what drainProbe saw of the listener's close: when the refused
// dial that dates it began and returned, zero when none was refused, and when
// the last dial the listener accepted began.
type drainDate struct {
	began, refused, accepted time.Time
}

// drainProbe dials addr back to back until a dial is refused or deadline
// passes, each dial bounded by drainProbeDial and closed at once. A refusal's
// return follows the close: the kernel refuses a SYN only once no listener is
// left on the port. A dial that timed out proves nothing — FreeBSD answers a
// SYN that met the closing listener only on its retransmission, a second
// later (#1385) — and is dialled again, so the refusal kept is one the kernel
// answered at once.
func drainProbe(addr string, deadline time.Time) drainDate {
	var d drainDate
	//: until a refusal, bounded.
	for time.Now().Before(deadline) {
		start := time.Now()
		c, err := stdnet.DialTimeout("tcp", addr, drainProbeDial)
		end := time.Now()
		switch {
		//: the listener was still there when the SYN came.
		case err == nil:
			d.accepted = start
			swallowErr(c.Close())
		//: no listener left on the port.
		case errors.Is(err, syscall.ECONNREFUSED):
			d.began, d.refused = start, end
			return d
		}
	}
	return d
}

// TestDrain_graceBoundedByCtx pins that the grace never outlives the
// caller's budget: a Shutdown whose ctx ends first goes on with the drain.
func TestDrain_graceBoundedByCtx(t *testing.T) {
	srv := New(WithDrainGrace(time.Hour))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable})).HandleLoop(&laneEchoH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	_ = srv.Shutdown(ctx)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("Shutdown took %v with a 100ms budget", took)
	}
	if err := srv.readiness(context.Background()); err == nil {
		t.Fatal("ready after Shutdown")
	}
}

// TestDrain_stoppedOwesNoGrace pins that a second Shutdown, the server
// already stopped, returns without waiting the grace: nothing serves any more.
func TestDrain_stoppedOwesNoGrace(t *testing.T) {
	srv := New(WithDrainGrace(50 * time.Millisecond))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable})).HandleLoop(&laneEchoH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	began := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if took := time.Since(began); took >= 50*time.Millisecond {
		t.Fatalf("second Shutdown took %v: it waited the grace", took)
	}
}

// laneEchoH echoes each newline-terminated line at once. It keeps nothing per
// connection: a LoopConnRef names a connection within its lane only, and
// LoopsAuto runs several, so a map keyed by references across lanes — as
// loopLineH's queue is — would mix their connections up.
type laneEchoH struct {
	opens, closes atomic.Int64
}

func (h *laneEchoH) OnOpen(corenet.LoopIO, corenet.LoopConnRef) { h.opens.Add(1) }

func (h *laneEchoH) OnData(lio corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	used := 0
	for {
		i := bytes.IndexByte(buf[used:], '\n')
		if i < 0 {
			return used
		}
		_ = lio.Write(ref, buf[used:used+i+1])
		used += i + 1
	}
}

func (h *laneEchoH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)  {}
func (h *laneEchoH) OnWritable(corenet.LoopIO, corenet.LoopConnRef) {}
func (h *laneEchoH) OnBatchEnd(corenet.LoopIO)                      {}

func (h *laneEchoH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) { h.closes.Add(1) }

// oneExchange dials addr, sends one line, reads its echo and closes, and
// says what went wrong: empty when nothing did.
func oneExchange(addr string) string {
	_, msg, _ := exchange(addr)
	return msg
}

// exchange is oneExchange that also says when the dial returned and how it
// failed, nil when it succeeded.
func exchange(addr string) (time.Time, string, error) {
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	connected := time.Now()
	if err != nil {
		return connected, "dial: " + err.Error(), err
	}
	defer func() { swallowErr(c.Close()) }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("a\n")); err != nil {
		return connected, "write: " + err.Error(), nil
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "a\n" {
		return connected, "echo " + string(got) + ": " + errString(err), nil
	}
	return connected, "", nil
}

// lineEcho writes line on c and requires it back.
func lineEcho(t *testing.T, c stdnet.Conn, line string) {
	t.Helper()
	_, _ = c.Write([]byte(line))
	got := make([]byte, len(line))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != line {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

// errString is err's text, empty for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
