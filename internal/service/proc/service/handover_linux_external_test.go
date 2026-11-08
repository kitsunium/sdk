//go:build linux

package service_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The hand-over within one image (S0h, kitsunium/sdk#738): Freeze ends a unit
// without stopping its process, Resume makes it again from the hand-over —
// the same process, its restart counter, its store, its socket —, and the
// unit runs on: a crash restarts it and the store is handed again. A failed
// re-exec takes this path; the real one is handover_reexec_linux_external_test.
// Only the test's own processes are signalled.

// handoverConfig is the store unit of fdstore_linux_external_test, without
// its ExecStartPre=: a notify service keeping a file in its store,
// restarted at once after a crash. src is its socket, nil for a resumed one.
func handoverConfig(dir string, src coresvc.NotifySource) (service.Config, error) {
	main, err := coresvc.ParseCommand([]string{os.Args[0], "-test.run=^$"})
	if err != nil {
		return service.Config{}, err
	}
	return service.Config{
		Name: "store", Type: coresvc.TypeNotify, Notify: src, FileDescriptorStoreMax: 2,
		Restart: coresvc.RestartAlways, RestartDelay: 10 * time.Millisecond,
		StartLimitBurst: restartBurst, StartLimitInterval: restartWindow,
		TimeoutStart: 20 * time.Second, TimeoutStop: 10 * time.Second,
		Spec:      coreproc.Spec{Env: helperEnviron("fdstore", storeDirEnv+"="+dir)},
		ExecStart: []coresvc.CommandValue{main},
	}, nil
}

// closeUnit closes u on a failed test's way out, so no restart follows the
// kill of its process; a frozen or closed unit has nothing to close.
func closeUnit(u *service.Unit) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = u.Close(ctx)
}

// crashed kills u's main process — the test's own child — and waits until
// the unit is up again with restarts restarts and one stored descriptor: the
// helper's later runs keep "state" alone.
func crashed(ctx context.Context, u *service.Unit, restarts uint32) (coresvc.StateValue, error) {
	main := u.State().MainPID
	if err := syscall.Kill(main, syscall.SIGKILL); err != nil {
		return coresvc.StateValue{}, err
	}
	return upAgain(ctx, u, restarts, main, 1)
}

// upAgain waits until u is active with restarts restarts, a main process
// other than old, and stored descriptors in its store.
func upAgain(ctx context.Context, u *service.Unit, restarts uint32, old, stored int) (coresvc.StateValue, error) {
	for {
		st := u.State()
		if st.State == coresvc.StateActive && st.Restarts == restarts && st.MainPID != old && st.MainPID > 0 && len(u.FDStore()) == stored {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// runsService reports whether pid still runs this test's service: its environment
// holds dir, which no other process's does.
func runsService(pid int, dir string) bool {
	env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return err == nil && bytes.Contains(env, []byte(storeDirEnv+"="+dir))
}

// TestHandoverInProcess: Freeze then Resume within one image keep the unit's
// process, its restart counter, its generation and its store; the unit runs
// on, its store handed to the next run; no descriptor and no goroutine
// outlives the unit.
func TestHandoverInProcess(t *testing.T) {
	before := settledDescriptors(t)
	goroutines := runtime.NumGoroutine()
	dir := t.TempDir()
	t.Cleanup(func() { killHolders(t, dir) })
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	cfg, err := handoverConfig(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("NewUnit: %v", err)
	}
	t.Cleanup(func() { closeUnit(u) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err = upAgain(ctx, u, 0, 0, 2); err != nil {
		t.Fatalf("first run: %v", err)
	}
	st, err := crashed(ctx, u, 1)
	if err != nil {
		t.Fatalf("after a crash: %v (%+v)", err, st)
	}
	stored := u.FDStore()
	h, err := service.Freeze(ctx, []*service.Unit{u})
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background(), time.Second) })
	if got := h.Units(); len(got) != 1 || got[0] != "store" {
		t.Fatalf("Units = %v", got)
	}
	if err = u.Start(ctx); !errs.HasCode(err, coresvc.CodeUnitClosed) {
		t.Fatalf("a frozen unit took a start: %v", err)
	}
	if !runsService(st.MainPID, dir) {
		t.Fatalf("Freeze stopped the main process %d", st.MainPID)
	}
	cfg2, err := handoverConfig(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := service.Resume(&cfg2, h)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err = service.Resume(&cfg2, h); !errs.HasCode(err, coresvc.CodeHandoverInvalid) {
		t.Fatalf("a second Resume of the unit: %v, want HANDOVER_INVALID", err)
	}
	got := r.State()
	if got.MainPID != st.MainPID || got.Restarts != 1 || got.State != coresvc.StateActive || got.Generation != st.Generation {
		t.Fatalf("resumed %+v, frozen %+v", got, st)
	}
	if now := r.FDStore(); len(now) != 1 || now[0] != stored[0] {
		t.Fatalf("store resumed %+v, frozen %+v", now, stored)
	}
	_ = os.Remove(filepath.Join(dir, "handed"))
	if st, err = crashed(ctx, r, 2); err != nil {
		t.Fatalf("a crash after the hand-over: %v (%+v)", err, st)
	}
	handed, err := os.ReadFile(filepath.Join(dir, "handed"))
	if err != nil || !bytes.Contains(handed, []byte("state-1")) {
		t.Fatalf("the run after the hand-over was handed %q, %v", handed, err)
	}
	main := st.MainPID
	if err = r.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err = h.Close(ctx, time.Second); err != nil {
		t.Fatalf("Handover.Close: %v", err)
	}
	if runsService(main, dir) {
		t.Fatalf("the main process %d outlived Close", main)
	}
	sameDescriptors(t, "freeze and resume", before, settledDescriptors(t))
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > goroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Errorf("%d goroutines after the hand-over, %d before", n, goroutines)
	}
}

// TestHandoverCloseStopsWhatNobodyResumed: a frozen unit no Resume takes is
// stopped in order by the hand-over's Close — never left running.
func TestHandoverCloseStopsWhatNobodyResumed(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { killHolders(t, dir) })
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	cfg, err := handoverConfig(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("NewUnit: %v", err)
	}
	t.Cleanup(func() { closeUnit(u) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	main := u.State().MainPID
	h, err := service.Freeze(ctx, []*service.Unit{u})
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err = h.Close(ctx, 5*time.Second); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if runsService(main, dir) || len(h.Units()) != 0 {
		t.Fatalf("after Close: main %d alive %v, units %v", main, runsService(main, dir), h.Units())
	}
	if _, err = service.Freeze(ctx, []*service.Unit{u}); !errs.HasCode(err, coresvc.CodeUnitClosed) {
		t.Fatalf("Freeze of a frozen unit: %v, want UNIT_CLOSED", err)
	}
}

// waitUntil polls cond until it holds or ctx ends.
func waitUntil(ctx context.Context, cond func() bool) error {
	for !cond() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}

// frozenAndResumed freezes u and returns the unit Resume makes from it, with
// cfg's changes applied to the resumed configuration; both are closed when the
// test ends.
func frozenAndResumed(ctx context.Context, t *testing.T, u *service.Unit, dir string, change func(*service.Config)) *service.Unit {
	t.Helper()
	h, err := service.Freeze(ctx, []*service.Unit{u})
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background(), time.Second) })
	cfg, err := handoverConfig(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	change(&cfg)
	r, err := service.Resume(&cfg, h)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Cleanup(func() { closeUnit(r) })
	return r
}

// TestHandoverDuringTheRestartDelay: a unit frozen while Restart= waits comes
// back still waiting and restarts at its deadline, not at the Resume and not
// never: the timer crosses the hand-over.
func TestHandoverDuringTheRestartDelay(t *testing.T) {
	const delay = 1500 * time.Millisecond
	dir := t.TempDir()
	t.Cleanup(func() { killHolders(t, dir) })
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	cfg, err := handoverConfig(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RestartDelay = delay
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("NewUnit: %v", err)
	}
	t.Cleanup(func() { closeUnit(u) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err = upAgain(ctx, u, 0, 0, 2); err != nil {
		t.Fatalf("first run: %v", err)
	}
	main := u.State().MainPID
	killed := time.Now()
	if err = syscall.Kill(main, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", main, err)
	}
	if err = waitUntil(ctx, func() bool { return u.State().State == coresvc.StateAutoRestart }); err != nil {
		t.Fatalf("the crash was not seen: %v (%+v)", err, u.State())
	}
	r := frozenAndResumed(ctx, t, u, dir, func(c *service.Config) { c.RestartDelay = delay })
	if st := r.State(); st.State != coresvc.StateAutoRestart {
		t.Fatalf("resumed in %+v, want still waiting out the delay", st)
	}
	st, err := upAgain(ctx, r, 1, main, 1)
	if err != nil {
		t.Fatalf("no restart after the delay: %v (%+v)", err, st)
	}
	if elapsed := time.Since(killed); elapsed < delay {
		t.Fatalf("restarted %s after the crash, before its %s delay: the deadline did not cross the hand-over", elapsed, delay)
	}
}

// TestHandoverKeepsTheStartLimit: the starts a unit counted before a Freeze
// still count after its Resume, and the limiter refuses the start they exhaust.
func TestHandoverKeepsTheStartLimit(t *testing.T) {
	// The first start, a restart before the hand-over and one after it fill
	// the burst; the next start is refused.
	const burst = 3
	dir := t.TempDir()
	t.Cleanup(func() { killHolders(t, dir) })
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	cfg, err := handoverConfig(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	cfg.StartLimitBurst = burst
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("NewUnit: %v", err)
	}
	t.Cleanup(func() { closeUnit(u) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err = upAgain(ctx, u, 0, 0, 2); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if st, cerr := crashed(ctx, u, 1); cerr != nil {
		t.Fatalf("the restart before the hand-over: %v (%+v)", cerr, st)
	}
	r := frozenAndResumed(ctx, t, u, dir, func(c *service.Config) { c.StartLimitBurst = burst })
	if st, cerr := crashed(ctx, r, 2); cerr != nil {
		t.Fatalf("the restart after the hand-over, the third start: %v (%+v)", cerr, st)
	}
	main := r.State().MainPID
	if err = syscall.Kill(main, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", main, err)
	}
	limited := func() bool { return r.State().Result == coresvc.ResultStartLimitHit }
	if err = waitUntil(ctx, limited); err != nil {
		t.Fatalf("the fourth start was not refused: %v (%+v)", err, r.State())
	}
	if st := r.State(); st.State != coresvc.StateFailed || st.MainPID != 0 {
		t.Fatalf("after the refused start: %+v, want failed, no main process", st)
	}
}
