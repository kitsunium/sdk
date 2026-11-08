//go:build !windows

package service

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The replay of the descriptor store (S0d, kitsunium/sdk#736): what FDSTORE=1
// keeps, refuses, removes and hands back, and when the store is released.

// pipes returns n descriptors the harness can pass around, closed at the
// test's end: the harness itself closes nothing.
func pipes(t *testing.T, n int) []*os.File {
	t.Helper()
	out := make([]*os.File, 0, n)
	for range n {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
		t.Cleanup(func() { _ = r.Close() })
		out = append(out, r)
	}
	return out
}

// notifyFiles sends a message carrying files from pid.
func (h *harness) notifyFiles(pid int, files []*os.File, dupOf []uint64, fields ...string) {
	f := map[string]string{}
	for _, kv := range fields {
		k, v, _ := strings.Cut(kv, "=")
		f[k] = v
	}
	h.feed(event{kind: evNotify, dupOf: dupOf, msg: coresvc.NotifyValue{Fields: f, Files: files, PID: pid, UID: 1000, Verified: true}})
}

// names lists the store's names, oldest first.
func (h *harness) names() []string {
	var out []string
	for _, e := range h.m.store {
		out = append(out, e.name)
	}
	return out
}

// storing is a simple unit with a store of max descriptors.
func storing(t *testing.T, max int) Config {
	return Config{Notify: stubNotify{}, FileDescriptorStoreMax: max, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}}
}

// lastSnap is the last snapshot published.
func (h *harness) lastSnap() coresvc.StateValue {
	h.t.Helper()
	if len(h.published) == 0 {
		h.t.Fatal("nothing published")
	}
	return h.published[len(h.published)-1]
}

// TestStoreKeepsNamesRefusesRemoves: FDSTORE=1 keeps under FDNAME= — an
// invalid name is "stored" —, past FileDescriptorStoreMax= the rest is closed
// and counted, FDSTOREREMOVE=1 closes a name; every change is published.
func TestStoreKeepsNamesRefusesRemoves(t *testing.T) {
	h := newHarness(t, storing(t, 3))
	h.start()
	main := h.m.mainPID
	fs := pipes(t, 6)
	h.notifyFiles(main, fs[:2], nil, "FDSTORE=1", "FDNAME=a")
	h.notifyFiles(main, fs[2:3], nil, "FDSTORE=1", "FDNAME=bad:name")
	if got := h.names(); !slices.Equal(got, []string{"a", "a", "stored"}) {
		t.Fatalf("store %v", got)
	}
	h.notifyFiles(main, fs[3:5], nil, "FDSTORE=1", "FDNAME=b")
	if len(h.m.store) != 3 || h.m.storeRefused != 2 || h.lastSnap().FDStoreRefused != 2 || h.lastSnap().FDStore != 3 {
		t.Fatalf("past the maximum: %d stored, %d refused, snapshot %+v", len(h.m.store), h.m.storeRefused, h.lastSnap())
	}
	if !slices.Equal(h.closed, fs[3:5]) {
		t.Fatalf("closed %v, want the two refused", h.closed)
	}
	h.notifyFiles(main, fs[5:6], nil, "FDSTOREREMOVE=1", "FDNAME=a")
	if got := h.names(); !slices.Equal(got, []string{"stored"}) || len(h.dropped) != 2 || h.lastSnap().FDStore != 1 {
		t.Fatalf("after FDSTOREREMOVE=1: %v, %d dropped", got, len(h.dropped))
	}
	if !slices.Contains(h.closed, fs[5]) {
		t.Fatal("a descriptor without FDSTORE=1 was not closed")
	}
	h.notifyFiles(main, nil, nil, "FDSTOREREMOVE=1")
	if len(h.m.store) != 1 {
		t.Fatal("FDSTOREREMOVE=1 without FDNAME= removed something")
	}
}

// TestStoreClosesWhatItDoesNotKeep: an unadmitted sender's descriptors, a
// BARRIER=1's, MAINPIDFD=1's pidfd, a duplicate of a stored object or of an
// earlier one, and every descriptor sent to a unit without a store — closed,
// none kept.
func TestStoreClosesWhatItDoesNotKeep(t *testing.T) {
	h := newHarness(t, storing(t, 8))
	h.start()
	main := h.m.mainPID
	fs := pipes(t, 8)
	h.notifyFiles(main+1, fs[:1], nil, "FDSTORE=1")
	h.notifyFiles(main, fs[1:2], nil, "BARRIER=1")
	h.notifyFiles(main, fs[2:4], nil, "MAINPIDFD=1", "FDSTORE=1", "FDNAME=p")
	if len(h.m.store) != 1 || h.kept[0].name != "p" || h.m.notifyRefused != 1 {
		t.Fatalf("store %v, refused %d", h.names(), h.m.notifyRefused)
	}
	if !slices.Equal(h.closed, fs[:3]) {
		t.Fatalf("closed %v, want the unadmitted, the barrier and the pidfd", h.closed)
	}
	stored := h.m.store[0].id
	h.notifyFiles(main, fs[4:7], []uint64{stored, 0, dupEarlier}, "FDSTORE=1")
	if len(h.m.store) != 2 || !slices.Contains(h.closed, fs[4]) || !slices.Contains(h.closed, fs[6]) || slices.Contains(h.closed, fs[5]) {
		t.Fatalf("duplicates: store %v, closed %v", h.names(), h.closed)
	}
	none := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	none.start()
	none.notifyFiles(none.m.mainPID, fs[7:8], nil, "FDSTORE=1")
	if len(none.m.store) != 0 || none.m.storeRefused != 1 || !slices.Equal(none.closed, fs[7:8]) {
		t.Fatalf("a unit without a store: %d stored, %d refused", len(none.m.store), none.m.storeRefused)
	}
}

// TestStoreHandedToExecStartOnly: after a crash the store goes to the new
// ExecStart= — newest first — and to no command: neither ExecStartPre=,
// ExecStartPost= nor ExecStop=.
func TestStoreHandedToExecStartOnly(t *testing.T) {
	cfg := storing(t, 4)
	cfg.Restart = coresvc.RestartAlways
	cfg.ExecStartPre = []coresvc.CommandValue{cmd(t, "/bin/pre")}
	cfg.ExecStartPost = []coresvc.CommandValue{cmd(t, "/bin/post")}
	cfg.ExecStop = []coresvc.CommandValue{cmd(t, "/bin/stop")}
	h := newHarness(t, cfg)
	h.start()
	h.exit(h.m.controlPID, 0)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	fs := pipes(t, 2)
	h.notifyFiles(h.m.mainPID, fs[:1], nil, "FDSTORE=1", "FDNAME=old")
	h.notifyFiles(h.m.mainPID, fs[1:], nil, "FDSTORE=1", "FDNAME=new")
	h.spawns = nil
	h.exit(h.m.mainPID, 1)
	h.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	if len(h.m.store) != 2 || len(h.dropped) != 0 {
		t.Fatalf("the store did not survive the crash: %v, dropped %v", h.names(), h.dropped)
	}
	h.advance(time.Second)
	h.exit(h.m.controlPID, 0)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.request(evStop)
	for _, ef := range h.spawns {
		want := 0
		if ef.list == listStart {
			want = 2
		}
		if len(ef.entries) != want {
			t.Errorf("%s was handed %d descriptors, want %d", listDirectives[ef.list], len(ef.entries), want)
		}
	}
	main := h.spawnsOf(listStart)
	if len(main) != 1 || main[0].entries[0].name != "new" || main[0].entries[1].name != "old" {
		t.Fatalf("ExecStart= was handed %v, want new then old", main)
	}
}

// TestStorePreserve: FileDescriptorStorePreserve= — restart keeps the store
// through a restart and releases it once the unit rests dead; no releases it
// at every end of a run; yes keeps it across a stop until Close; on-success
// keeps it across a clean stop and releases it once failed for good.
func TestStorePreserve(t *testing.T) {
	run := func(preserve coresvc.Preserve, restart coresvc.Restart) *harness {
		cfg := storing(t, 4)
		cfg.FileDescriptorStorePreserve, cfg.Restart = preserve, restart
		h := newHarness(t, cfg)
		h.start()
		h.notifyFiles(h.m.mainPID, pipes(t, 1), nil, "FDSTORE=1")
		return h
	}
	r := run(coresvc.PreserveRestart, coresvc.RestartAlways)
	r.exit(r.m.mainPID, 1)
	r.advance(time.Second)
	restart := r.request(evRestart)
	r.kill(r.m.mainPID, sigTERM)
	r.wantReply(restart, 0)
	if len(r.m.store) != 1 {
		t.Fatal("restart: the store did not survive a crash and a restart job")
	}
	r.stopAndExit()
	if len(r.m.store) != 0 || len(r.dropped) != 1 {
		t.Fatalf("restart: a stopped unit kept %d", len(r.m.store))
	}

	n := run(coresvc.PreserveNo, coresvc.RestartAlways)
	n.exit(n.m.mainPID, 1)
	if len(n.m.store) != 0 || n.m.state != coresvc.StateAutoRestart {
		t.Fatalf("no: the store survived the end of a run (%d, %s)", len(n.m.store), n.m.state)
	}

	y := run(coresvc.PreserveYes, coresvc.RestartNo)
	y.stopAndExit()
	y.want(coresvc.StateInactive, coresvc.SubDead)
	y.spawns = nil
	y.start()
	if len(y.m.store) != 1 || len(y.spawnsOf(listStart)[0].entries) != 1 {
		t.Fatal("yes: the store did not survive a stop and reach the next start")
	}

	o := run(coresvc.PreserveOnSuccess, coresvc.RestartNo)
	o.stopAndExit()
	if len(o.m.store) != 1 {
		t.Fatal("on-success: a clean stop released the store")
	}
	o.start()
	o.exit(o.m.mainPID, 3)
	o.want(coresvc.StateFailed, coresvc.SubFailed)
	if len(o.m.store) != 0 {
		t.Fatal("on-success: a unit failed for good kept its store")
	}
}

// TestStoreDiscardsAForeignStore: a store filled for a unit this one no longer
// is — what S0h restores may be — is closed whole, never handed over, and
// counted; one filled since is the unit's own.
func TestStoreDiscardsAForeignStore(t *testing.T) {
	cfg := storing(t, 4)
	cfg.Restart = coresvc.RestartAlways
	h := newHarness(t, cfg)
	h.start()
	h.notifyFiles(h.m.mainPID, pipes(t, 2), nil, "FDSTORE=1")
	h.m.storeKey = "unit/another-ExecStart"
	h.spawns = nil
	h.exit(h.m.mainPID, 1)
	h.advance(time.Second)
	main := h.spawnsOf(listStart)
	if len(main) != 1 || len(main[0].entries) != 0 || len(h.m.store) != 0 || len(h.dropped) != 2 {
		t.Fatalf("a foreign store reached ExecStart=: %v, store %d, dropped %d", main, len(h.m.store), len(h.dropped))
	}
	if h.m.storeDiscarded != 2 || h.lastSnap().FDStoreDiscarded != 2 {
		t.Fatalf("discarded %d, snapshot %d", h.m.storeDiscarded, h.lastSnap().FDStoreDiscarded)
	}
	h.notifyFiles(h.m.mainPID, pipes(t, 1), nil, "FDSTORE=1")
	if h.m.storeKey != h.m.s.storeKey || len(h.m.store) != 1 {
		t.Fatalf("a store filled since is not the unit's: %q", h.m.storeKey)
	}
	h.m.storeKey = "unit/another-user"
	h.notifyFiles(h.m.mainPID, pipes(t, 1), nil, "FDSTORE=1")
	if len(h.m.store) != 1 || h.m.storeDiscarded != 3 {
		t.Fatalf("a foreign store was added to: %d stored, %d discarded", len(h.m.store), h.m.storeDiscarded)
	}
}

// TestStoreHangup: a stored descriptor's hangup removes it; a hangup for one
// gone already changes nothing.
func TestStoreHangup(t *testing.T) {
	h := newHarness(t, storing(t, 4))
	h.start()
	h.notifyFiles(h.m.mainPID, pipes(t, 2), nil, "FDSTORE=1")
	first := h.m.store[0].id
	h.feed(event{kind: evHangup, stored: first})
	if len(h.m.store) != 1 || len(h.dropped) != 1 || h.dropped[0].id != first {
		t.Fatalf("after the hangup: %d stored, dropped %v", len(h.m.store), h.dropped)
	}
	h.feed(event{kind: evHangup, stored: first})
	if len(h.dropped) != 1 {
		t.Fatal("a second hangup of the same descriptor dropped again")
	}
	h.notifyFiles(h.m.mainPID, pipes(t, 1), nil, "FDSTORE=1", "FDPOLL=0")
	if h.m.store[1].poll || !h.m.store[0].poll {
		t.Fatal("FDPOLL=0 not kept, or the default not to poll")
	}
}

// TestStoreSettings: a negative maximum is refused, a store needs a
// NotifySource and implies NotifyAccess=main, every command gets $FDSTORE,
// and the store's key follows ExecStart=, the user and the groups.
func TestStoreSettings(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "/bin/d")}
	for _, cfg := range []Config{
		{FileDescriptorStoreMax: -1, Notify: stubNotify{}},
		{FileDescriptorStoreMax: 2},
	} {
		cfg.Name, cfg.ExecStart = "u", start
		if _, err := resolve(&cfg); !hasCode(err, coresvc.CodeConfigInvalid) || field(err, "directive") != "FileDescriptorStoreMax" {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
	cfg := storing(t, 2)
	cfg.ExecStartPre = []coresvc.CommandValue{cmd(t, "/bin/pre")}
	h := newHarness(t, cfg)
	if h.m.s.notifyAccess != coresvc.NotifyAccessMain {
		t.Fatalf("a store implies NotifyAccess=%s", h.m.s.notifyAccess)
	}
	h.start()
	h.exit(h.m.controlPID, 0)
	for _, ef := range h.spawns {
		if !hasVar(ef, "FDSTORE=2") {
			t.Errorf("%s without $FDSTORE: %v", listDirectives[ef.list], ef.vars)
		}
	}
	base := storing(t, 2)
	base.Name = "u"
	key := storeKey(&base, nil)
	for name, change := range map[string]func(*Config){
		"ExecStart": func(c *Config) { c.ExecStart = []coresvc.CommandValue{cmd(t, "/bin/d", "-v")} },
		"User":      func(c *Config) { c.Spec.User = "nobody" },
		"Groups":    func(c *Config) { c.Spec.Groups = []string{"adm"} },
		"Name":      func(c *Config) { c.Name = "v" },
	} {
		other := base
		change(&other)
		if storeKey(&other, nil) == key {
			t.Errorf("another %s keeps the store's key", name)
		}
	}
	same := base
	same.Restart, same.Spec.Dir = coresvc.RestartAlways, "/srv"
	if storeKey(&same, nil) != key {
		t.Error("a directive that is not the main process's identity changed the store's key")
	}
}
