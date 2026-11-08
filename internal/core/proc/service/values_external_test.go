package service_test

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// TestNames: every value spells as systemd does, and an undefined one stays
// legible.
func TestNames(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{coresvc.TypeNotifyReload.String(), "notify-reload"},
		{coresvc.Type(99).String(), "type(99)"},
		{coresvc.RestartOnAbnormal.String(), "on-abnormal"},
		{coresvc.Restart(9).String(), "restart(9)"},
		{coresvc.RestartModeDirect.String(), "direct"},
		{coresvc.KillModeControlGroup.String(), "control-group"},
		{coresvc.KillModeDefault.String(), ""},
		{coresvc.KillModeProcess.String(), "process"},
		{coresvc.KillModeNone.String(), "none"},
		{coresvc.ExitTypeCgroup.String(), "cgroup"},
		{coresvc.NotifyAccessExec.String(), "exec"},
		{coresvc.TimeoutAbort.String(), "abort"},
		{coresvc.OOMPolicyKill.String(), "kill"},
		{coresvc.PreserveOnSuccess.String(), "on-success"},
		{coresvc.StateAutoRestart.String(), "auto-restart"},
		{coresvc.StateAutoRestart.ActiveState(), "activating"},
		{coresvc.StateExited.ActiveState(), "active"},
		{coresvc.StateConditionSkipped.ActiveState(), "inactive"},
		{coresvc.SubStopSigterm.String(), "stop-sigterm"},
		{coresvc.ResultStartLimitHit.String(), "start-limit-hit"},
		{coresvc.ResultExecCondition.String(), "exec-condition"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
	if !coresvc.ToggleDefault.On(true) || coresvc.ToggleNo.On(true) || !coresvc.ToggleYes.On(false) {
		t.Error("Toggle.On")
	}
}

// TestExitStatusSet: codes match an exit, signals a signal.
func TestExitStatusSet(t *testing.T) {
	segv := coreproc.Signal(syscall.SIGSEGV)
	s := coresvc.ExitStatusValue{Codes: []int{75}, Signals: []coreproc.Signal{segv}}
	if !s.Contains(coreproc.ExitValue{Code: 75}) || s.Contains(coreproc.ExitValue{Code: 1}) {
		t.Error("codes")
	}
	if !s.Contains(coreproc.ExitValue{Signal: segv, Signaled: true}) || s.Contains(coreproc.ExitValue{Code: int(segv)}) {
		t.Error("signals")
	}
	if s.Empty() || !(coresvc.ExitStatusValue{}).Empty() {
		t.Error("Empty")
	}
}
