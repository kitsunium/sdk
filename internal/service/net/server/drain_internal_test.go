// Package server — the drain (plan P6): readiness first, the grace, then the
// listeners, under continuous arrivals; and LoopsAuto's snapshot in State.
package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"os"
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
//
// An arrival is dated by what the kernel did with it, not by when its dial
// began (#1556): a dial refused once the listener may have closed — its
// refusal returned drainGraceTest or more after Shutdown began — had no SYN
// the listener answered, since a listening socket answers every SYN with a
// SYN|ACK at once (FreeBSD's tcp_syncache.c, syncache_add) and a kernel
// refuses one only once no listener is left; the server never had that
// connection, so it owed it nothing. FreeBSD's guest showed one: begun 48 ms
// into the grace, its first SYN unanswered, refused 1.03 s later on the
// retransmission. Any other failed dial, a refusal inside the grace, and every
// connection the kernel completed stay faults. Each late/ case forces that
// order: one more client dates its dial inside the grace and sends its SYN
// once the listener is closed.
func TestDrain_graceUnderArrivals(t *testing.T) {
	type tc struct {
		name    string
		p       drainProto
		b       corenet.LoopBackend
		lateSYN bool
	}
	var cases []tc
	for _, p := range drainProtos() {
		for _, b := range loopBackends() {
			cases = append(cases, tc{name: p.name + "/" + b.String(), p: p, b: b})
			//: a dial's date is the protocol's no more than the backend's: once
			//: per backend, on the line echo.
			if p.name == "line" {
				cases = append(cases, tc{name: "late/" + p.name + "/" + b.String(), p: p, b: b, lateSYN: true})
			}
		}
	}
	runCase := func(t *testing.T, c tc) { drainGraceUnderArrivals(t, c.p, c.b, c.lateSYN) }
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// drainGraceUnderArrivals is TestDrain_graceUnderArrivals on protocol p and
// backend b; with lateSYN, one more client begins its dial inside the grace
// and sends its SYN only once a probe was refused: the listener closed.
func drainGraceUnderArrivals(t *testing.T, p drainProto, b corenet.LoopBackend, lateSYN bool) {
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
	var late, unanswered atomic.Int64
	// shutAt is when Shutdown was called, in nanoseconds: the listener closes
	// no earlier than drainGraceTest after it.
	var shutAt atomic.Int64
	errc := make(chan string, 64)
	// failed judges a dial begun at t0 that went wrong with msg, derr its
	// dial's error: it reports whether the client stops.
	failed := func(t0 time.Time, msg string, derr error) bool {
		//: nothing went wrong.
		if msg == "" {
			return false
		}
		ended := time.Now()
		//: refused once the listener may have closed: no SYN of it was
		//: answered while the listener listened, so none was owed (#1556).
		if s := shutAt.Load(); s != 0 && dialRefused(derr) && !ended.Before(time.Unix(0, s).Add(drainGraceTest)) {
			unanswered.Add(1)
			t.Logf("refused once the listener may have closed, no SYN of it answered while it listened: %s began %s, ended %s", msg, t0.Format("15:04:05.000"), ended.Format("15:04:05.000"))
			return true
		}
		select {
		case errc <- msg + " began " + t0.Format("15:04:05.000") + ", ended " + ended.Format("15:04:05.000") + ", readiness false " + strconv.FormatBool(notReady.Load()):
		default:
		}
		return true
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				after := notReady.Load()
				t0 := time.Now()
				//: a failure ends this client.
				if _, msg, derr := p.exchange(addr); failed(t0, msg, derr) {
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
	//: the order FreeBSD's guest showed, forced: a dial dated inside the
	//: grace whose SYN the kernel first answers once the listener closed.
	if lateSYN {
		wg.Go(func() {
			//: until readiness turned false: Shutdown began.
			for !notReady.Load() {
				time.Sleep(time.Millisecond)
			}
			t0 := time.Now()
			//: the listener never closed within the probe's bound.
			if d := drainProbe(addr, t0.Add(10*time.Second)); d.refused.IsZero() {
				failed(t0, "late: no probe refused within 10s", nil)
				return
			}
			_, msg, derr := p.exchange(addr)
			//: a listener that closed as it should refuses this dial.
			if msg == "" {
				msg = "dial: accepted after a refused probe"
			}
			failed(t0, msg, derr)
		})
	}
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
	shutAt.Store(began.UnixNano())
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
	//: the forced order must have been met, and judged the kernel's.
	if lateSYN && unanswered.Load() == 0 {
		t.Fatal("late: the dial sent once the listener closed was not refused")
	}
	if o, cl := counts(); o != cl {
		t.Fatalf("%d opened, %d closed", o, cl)
	}
}

// wsaeConnRefused is WSAECONNREFUSED (winerror.h, 10061): what Windows'
// connectex returns for a refused connection. Package syscall's
// ECONNREFUSED on Windows is a value of its own, never returned by a socket
// call, and Errno.Is does not map one onto the other: the windows job's
// "late: no probe refused within 10s" (#1556).
const wsaeConnRefused = syscall.Errno(10061)

// dialRefused reports whether a dial failed because the kernel refused it: no
// listener on the port. ECONNREFUSED everywhere, and WSAECONNREFUSED on
// Windows.
func dialRefused(err error) bool {
	//: Windows' own errno, there only.
	return errors.Is(err, syscall.ECONNREFUSED) || (runtime.GOOS == "windows" && errors.Is(err, wsaeConnRefused))
}

// TestDrain_refusedIsTheKernelsRefusal pins dialRefused on errors wrapped as a
// dial wraps them: ECONNREFUSED is a refusal everywhere, WSAECONNREFUSED on
// Windows only, and neither a timeout nor a reset is one.
func TestDrain_refusedIsTheKernelsRefusal(t *testing.T) {
	type tc struct {
		name string
		err  error
		want bool
	}
	dial := func(err error) error {
		return &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	cases := []tc{
		{name: "ECONNREFUSED", err: dial(syscall.ECONNREFUSED), want: true},
		{name: "WSAECONNREFUSED", err: dial(wsaeConnRefused), want: runtime.GOOS == "windows"},
		{name: "timeout", err: dial(os.ErrDeadlineExceeded)},
		{name: "reset", err: dial(syscall.ECONNRESET)},
		{name: "nil"},
	}
	runCase := func(t *testing.T, c tc) {
		//: what dialRefused says against what the platform's kernel means.
		if got := dialRefused(c.err); got != c.want {
			t.Fatalf("dialRefused(%v) = %v on %s, want %v", c.err, got, runtime.GOOS, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
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
// what waited there is served. The grace lasts until one arrival that began
// in it was served (graceHeld), so at least one crosses the drain whatever
// the guest's scheduling took of its 50 ms.
func TestDrain_arrivalsAcrossTheDrain(t *testing.T) {
	runCase := func(t *testing.T, c drainArrivalsCase) { drainArrivalsAcross(t, c) }
	for _, c := range drainArrivalsCases("", 0, 0) {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// drainStallTest is how long TestDrain_arrivalsAcrossAStalledGrace holds its
// clients once the drain began: three times the grace drainArrivalsAcross
// gives, as a guest whose clients were descheduled for the whole grace.
// drainLagShutdown is how late its second case calls Shutdown once it began
// dialling for the drain: longer than the stall, so a client held from the
// test's own start, not the server's, would be served before the drain.
const (
	drainStallTest   = 150 * time.Millisecond
	drainLagShutdown = 200 * time.Millisecond
)

// TestDrain_arrivalsAcrossAStalledGrace is TestDrain_arrivalsAcrossTheDrain
// with its clients held for drainStallTest once the drain began: what a
// loaded guest does to them for longer than the grace. The drain serves
// every arrival all the same, so the test still sees them cross: it holds
// the grace until one that began in it was served (graceHeld), and does not
// read a stall of its own clients as a drain that serves nothing — "0 served
// during it, 4 refused after" on macOS's kqueue and under load on the
// portable backend (#1385). Its second case calls Shutdown drainLagShutdown
// late: an arrival counts as crossing only once the server's phase is
// PhaseDraining, never on the test's word that it called Shutdown.
func TestDrain_arrivalsAcrossAStalledGrace(t *testing.T) {
	runCase := func(t *testing.T, c drainArrivalsCase) { drainArrivalsAcross(t, c) }
	cases := append(drainArrivalsCases("", drainStallTest, 0), drainArrivalsCases("late/", drainStallTest, drainLagShutdown)...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// drainArrivalsCase is one case of the arrivals tests: protocol p on backend
// b, the clients held for stall once the server drains, Shutdown called lag
// after the test began to wait for the drain.
type drainArrivalsCase struct {
	name       string
	p          drainProto
	b          corenet.LoopBackend
	stall, lag time.Duration
}

// drainArrivalsCases is every protocol on every backend the platform has,
// named prefix+protocol/backend, with stall and lag.
func drainArrivalsCases(prefix string, stall, lag time.Duration) []drainArrivalsCase {
	var out []drainArrivalsCase
	for _, p := range drainProtos() {
		for _, b := range loopBackends() {
			out = append(out, drainArrivalsCase{name: prefix + p.name + "/" + b.String(), p: p, b: b, stall: stall, lag: lag})
		}
	}
	return out
}

// drainArrivalsAcross is TestDrain_arrivalsAcrossTheDrain on c.
func drainArrivalsAcross(t *testing.T, c drainArrivalsCase) {
	p, b, stall := c.p, c.b, c.stall
	if runtime.GOOS == "windows" {
		// ADR 0196 §D3: a Windows listener cannot be duplicated into the
		// portable backend's own, so its drain takes no queue and a close
		// under arrivals resets what waits; the grace test above holds.
		t.Skip("the portable backend takes no listen queue on Windows")
	}
	srv := New(WithDrainGrace(50 * time.Millisecond))
	// crossed holds the grace until an arrival that began in it was served:
	// the grace's 50 ms are the drain's, and a client stall that outlasts
	// them is the guest's, not a drain that serves nothing (#1385).
	crossed := make(chan struct{})
	var crossedOnce sync.Once
	srv.graceHeld = crossed
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
	// drainsNow reports whether the server drains: its phase, never the
	// test's word that it called Shutdown, which a descheduled caller makes
	// late (#1557).
	drainsNow := func() bool { return corenet.Phase(srv.phase.Load()) >= corenet.PhaseDraining }
	// drainAt is when a client first saw the server drain, in nanoseconds:
	// the stall's start, no earlier than the drain's.
	var drainAt atomic.Int64
	// beforeDrain counts the arrivals counted as crossing that began while
	// the server was not draining yet: none may be.
	var during, refused, beforeDrain atomic.Int64
	var mu sync.Mutex
	var cuts []cut
	// early holds the dials that failed before the drain began: faults of
	// their own, judged apart from the cuts the close's window may explain.
	var early []string
	// accepted is when the last dial the listener accepted began: the close
	// came after it. closed is the earliest return of a refused dial: the
	// close came before it.
	var accepted, closed time.Time
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			stalled := false
			for {
				began := drainsNow()
				//: the first to see the drain dates it.
				if began {
					drainAt.CompareAndSwap(0, time.Now().UnixNano())
				}
				//: held once, as a descheduled client is, from the drain's start.
				if began && !stalled && stall > 0 {
					stalled = true
					time.Sleep(time.Until(time.Unix(0, drainAt.Load()).Add(stall)))
				}
				//: a connection is placed by when the kernel completed it:
				//: an h2c exchange on a slow guest (NetBSD) began 20.036 ms
				//: before the close and was completed inside the window.
				start := time.Now()
				serving := corenet.Phase(srv.phase.Load()) < corenet.PhaseDraining
				at, msg, derr := p.exchange(addr)
				failed := time.Now()
				mu.Lock()
				if derr == nil && start.After(accepted) {
					accepted = start
				}
				switch {
				//: before the drain began: a fault whatever the close's date,
				//: never one of the close's window.
				case derr != nil && !began && !drainsNow():
					early = append(early, msg)
				case derr != nil:
					refused.Add(1)
					if dialRefused(derr) && (closed.IsZero() || failed.Before(closed)) {
						closed = failed
					}
				case msg != "":
					cuts = append(cuts, cut{at, failed, msg})
				case began:
					during.Add(1)
					//: counted as crossing, yet begun while the server served.
					if serving {
						beforeDrain.Add(1)
					}
					crossedOnce.Do(func() { close(crossed) })
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
	var probe drainDate
	wg.Go(func() {
		probe = drainProbe(addr, time.Now().Add(10*time.Second))
	})
	//: the Shutdown caller descheduled, as a loaded guest may.
	time.Sleep(c.lag)
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	wg.Wait()
	if n := beforeDrain.Load(); n > 0 {
		t.Fatalf("%d of %d arrivals counted as crossing the drain began before the server drained", n, during.Load())
	}
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
	// The close is known only between two dates: after accepted, when the
	// last dial the listener took began, and before closed, when the first
	// refusal returned — a worker descheduled after the close resumes, and
	// dates it, late. A cut is a fault only if it came drainCloseWindow
	// before every instant of that interval: before accepted's. One that came
	// within the window of closed is the close's; one in between is
	// undecided, its distance to each end logged.
	for _, msg := range early {
		t.Errorf("a dial failed before the drain began: %s", msg)
	}
	window, undecided := 0, 0
	for _, c := range cuts {
		switch {
		//: within the window of the latest date the close may have.
		case c.at.After(closed.Add(-drainCloseWindow)):
			window++
		//: within the window of an instant the close may have had.
		case !c.at.Before(accepted.Add(-drainCloseWindow)):
			undecided++
			t.Logf("cut %v before the close's latest date and %v before its earliest, the close dated within %v: %s", closed.Sub(c.at), accepted.Sub(c.at), closed.Sub(accepted), c.msg)
		default:
			t.Errorf("cut short %v before the listener closed, failed %v before it; the last dial the listener accepted began %v after this one returned: %s", closed.Sub(c.at), closed.Sub(c.failed), accepted.Sub(c.at), c.msg)
		}
	}
	t.Logf("%d served during the drain; %d reset within %v of the close, %d undecided; the close dated %v after the last dial accepted began, the probe's refusal taking %v", during.Load(), window, drainCloseWindow, undecided, closed.Sub(accepted), probe.refused.Sub(probe.began))
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
			resetConn(c)
		//: no listener left on the port.
		case dialRefused(err):
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
	defer resetConn(c)
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

// resetConn closes c with a reset (SO_LINGER 0) rather than a FIN, once the
// exchange on it is over: neither end keeps a TIME_WAIT socket. A TCP close
// leaves one on the side that closed first — the client after its answer, the
// server after a drain or a refused handshake —, and on macOS, its 16 384
// ephemeral ports each held 30 s, the arrivals across the drain, dialled back
// to back since #1430, and the handshake flood's 10 000 ran the range out:
// "can't assign requested address" (#1430's macOS run). Only once the server
// acknowledged everything the client sent — its answer or its end read after
// the client's last write —: FreeBSD takes a reset only at the sequence it
// last acknowledged (h2cSettled says what happens otherwise).
func resetConn(c stdnet.Conn) {
	//: a TCP connection: its close a reset.
	if tc, ok := c.(*stdnet.TCPConn); ok {
		swallowErr(tc.SetLinger(0))
	}
	swallowErr(c.Close())
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
