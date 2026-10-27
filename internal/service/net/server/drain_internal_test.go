// Package server — the drain (plan P6): readiness first, the grace, then the
// listeners, under continuous arrivals; and LoopsAuto's snapshot in State.
package server

import (
	"bytes"
	"context"
	"io"
	stdnet "net"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
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
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			srv := New(WithDrainGrace(drainGraceTest))
			t.Cleanup(func() { swallowErr(srv.Close()) })
			h := &laneEchoH{}
			srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: b, Loops: corenet.LoopsAuto})).HandleLoop(h)
			procs := runtime.GOMAXPROCS(0)
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			st := srv.State()
			if ls := st.Loops[0]; ls.Loops != procs || ls.LoopsSource != loopsAuto {
				t.Fatalf("LoopsAuto: loops %d (%q), want %d (%q)", ls.Loops, ls.LoopsSource, procs, loopsAuto)
			}
			addr := st.Listeners[0].Address

			kept := make([]stdnet.Conn, 8)
			for i := range kept {
				kept[i] = loopDial(t, addr)
				lineEcho(t, kept[i], "k\n")
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
						if msg := oneExchange(addr); msg != "" {
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
					rest, err := io.ReadAll(c)
					if err != nil || len(rest) > 0 {
						select {
						case errc <- "kept: " + errString(err) + " " + string(rest):
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
			if o, cl := h.opens.Load(), h.closes.Load(); o != cl {
				t.Fatalf("%d opened, %d closed", o, cl)
			}
		})
	}
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
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "dial: " + err.Error()
	}
	defer func() { swallowErr(c.Close()) }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("a\n")); err != nil {
		return "write: " + err.Error()
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "a\n" {
		return "echo " + string(got) + ": " + errString(err)
	}
	return ""
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
