//go:build windows

package service

import (
	"os"
	"slices"
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// TestWindowsContract is ADR 0214's Windows subset: a kill and nothing else.
// Every signal's zero is SIGKILL, another signal is refused, and so are
// SendSIGHUP=, WatchdogSec=, the notification types, Type=forking and an
// instance per connection; the reducer runs, and a stop kills the main
// process at once. The signal suites of the other platforms do not build
// here (reduce_internal_test.go, unit_external_test.go).
func TestWindowsContract(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "daemon.exe")}
	s, err := resolve(&Config{Name: "u", ExecStart: start})
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []any{s.killSignal, s.restartKillSignal, s.finalKillSignal, s.watchdogSig} {
		if sig != sigKILL {
			t.Fatalf("a signal resolves to %v, want SIGKILL", sig)
		}
	}
	for name, cfg := range map[string]Config{
		"KillSignal":  {KillSignal: sigTERM},
		"SendSIGHUP":  {SendSIGHUP: true},
		"WatchdogSec": {Watchdog: time.Second},
		"Type":        {Type: coresvc.TypeNotify},
	} {
		cfg.Name, cfg.ExecStart = "u", start
		if _, err := resolve(&cfg); !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "directive") != name {
			t.Errorf("%s: %v, want DIRECTIVE_UNSUPPORTED", name, err)
		}
	}
	fork := Config{Name: "u", Type: coresvc.TypeForking, PIDFile: `C:\d.pid`, Attacher: stubAttacher{}, ExecStart: start}
	if _, err := resolve(&fork); !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "value") != "forking" {
		t.Errorf("Type=forking: %v, want DIRECTIVE_UNSUPPORTED", err)
	}
	conn, err := os.CreateTemp(t.TempDir(), "conn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newInstance(&Config{Name: "u", ExecStart: start}, conn); !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "directive") != "Accept" {
		t.Errorf("an instance: %v, want DIRECTIVE_UNSUPPORTED", err)
	}
	if _, err = conn.Stat(); err == nil {
		t.Error("a refused instance left its connection open")
	}
	h := newHarness(t, Config{Restart: coresvc.RestartOnFailure, ExecStart: start})
	h.wantReply(h.start(), 0)
	main := h.m.mainPID
	stop := h.request(evStop)
	if got := h.sentTo(main); len(got) != 1 || got[0] != sigKILL {
		t.Fatalf("a stop sent %v, want SIGKILL alone", got)
	}
	h.kill(main, sigKILL)
	h.wantReply(stop, 0)
}

// TestWindowsMergeEnvNames: Windows' names ignore case, so a template's Path=
// is the unit's PATH and no second one is added; "=C:" and "=D:" are two
// variables, which the engine keeps.
func TestWindowsMergeEnvNames(t *testing.T) {
	got := mergeEnv([]string{"Path=C:\\bin", `=C:=C:\x`, `=D:=D:\y`}, []string{"MAINPID=1"})
	if want := []string{"Path=C:\\bin", `=C:=C:\x`, `=D:=D:\y`, "MAINPID=1"}; !slices.Equal(got, want) {
		t.Errorf("mergeEnv = %q, want %q", got, want)
	}
}
