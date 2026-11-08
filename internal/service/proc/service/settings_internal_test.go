package service

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// field is a refusal's field value.
func field(err error, name string) string {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return ""
	}
	for _, f := range e.Fields() {
		if f.Key() == name {
			return f.StringValue()
		}
	}
	return ""
}

// TestCgroupDirectivesRefused: S0b's refusals of the cgroup modes are gone
// exactly where S0g runs them (kitsunium/sdk#734's non-regression). With a
// Cgroup that is a CgroupUnit, KillMode=control-group and mixed,
// ExitType=cgroup, Delegate= and every OOMPolicy= resolve; without one —
// none, a CgroupKill, a CgroupKill that lists its members — each is
// DIRECTIVE_UNSUPPORTED naming the directive and what lifts the refusal.
// S0e's refusals of Type=dbus and BusName= are lifted: TestDbusSettings.
func TestCgroupDirectivesRefused(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "/bin/d")}
	for _, tc := range []struct {
		cfg       Config
		directive string
	}{
		{Config{KillMode: coresvc.KillModeControlGroup}, "KillMode"},
		{Config{KillMode: coresvc.KillModeMixed}, "KillMode"},
		{Config{ExitType: coresvc.ExitTypeCgroup}, "ExitType"},
		{Config{Delegate: true}, "Delegate"},
		{Config{OOMPolicy: coresvc.OOMPolicyContinue}, "OOMPolicy"},
		{Config{OOMPolicy: coresvc.OOMPolicyStop}, "OOMPolicy"},
		{Config{OOMPolicy: coresvc.OOMPolicyKill}, "OOMPolicy"},
	} {
		with := tc.cfg
		with.Name, with.ExecStart, with.Cgroup = "u", start, &unitStub{}
		if _, err := resolve(&with); err != nil {
			t.Errorf("%s with a CgroupUnit: %v", tc.directive, err)
		}
		for _, cg := range []coresvc.CgroupKill{nil, stubCgroup{}, stubMembers{}} {
			without := tc.cfg
			without.Name, without.ExecStart, without.Cgroup = "u", start, cg
			_, err := resolve(&without)
			if !hasCode(err, coresvc.CodeDirectiveUnsupported) || field(err, "directive") != tc.directive ||
				!strings.Contains(field(err, "issue"), "CgroupUnit") {
				t.Errorf("%s with %T: %v (directive %q, issue %q), want DIRECTIVE_UNSUPPORTED naming it", tc.directive, cg, err, field(err, "directive"), field(err, "issue"))
			}
		}
	}
}

// TestConfigInvalid: the refusals that name a directive.
func TestConfigInvalid(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "/bin/d")}
	for _, tc := range []struct {
		cfg       Config
		directive string
	}{
		{Config{Name: "", ExecStart: start}, "Name"},
		{Config{Name: "u"}, "ExecStart"},
		{Config{Name: "u", ExecStart: append(start, start...)}, "ExecStart"},
		{Config{Name: "u", ExecStart: start, Restart: 99}, "Restart"},
		{Config{Name: "u", ExecStart: start, TimeoutStop: -2}, "TimeoutStopSec"},
		{Config{Name: "u", ExecStart: start, StartLimitBurst: -1}, "StartLimitBurst"},
		{Config{Name: "u", ExecStart: start, RestartSteps: -1}, "RestartSteps"},
		{Config{Name: "u", ExecStart: start, SuccessExitStatus: coresvc.ExitStatusValue{Codes: []int{256}}}, "SuccessExitStatus"},
		{Config{Name: "u", ExecStart: start, KillSignal: coreproc.Signal(250)}, "KillSignal"},
		{Config{Name: "u", ExecStart: []coresvc.CommandValue{{Args: []string{"x"}}}}, "ExecStart"},
	} {
		_, err := resolve(&tc.cfg)
		if !hasCode(err, coresvc.CodeConfigInvalid) || field(err, "directive") != tc.directive {
			t.Errorf("%s: %v (directive %q)", tc.directive, err, field(err, "directive"))
		}
	}
}

// TestNotifyWithoutSource: a notify type needs a NotifySource — off Windows,
// where the type itself is refused.
func TestNotifyWithoutSource(t *testing.T) {
	_, err := resolve(&Config{Name: "u", Type: coresvc.TypeNotify, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	want, directive := coresvc.CodeConfigInvalid, "NotifyAccess"
	if runtime.GOOS == osWindows {
		want, directive = coresvc.CodeDirectiveUnsupported, "Type"
	}
	if !hasCode(err, want) || field(err, "directive") != directive {
		t.Fatalf("Type=notify without a NotifySource: %v", err)
	}
}

// TestDefaults: every zero resolves to its systemd default.
func TestDefaults(t *testing.T) {
	s, err := resolve(&Config{Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	if err != nil {
		t.Fatal(err)
	}
	kill, abort := sigTERM, sigABRT
	if runtime.GOOS == osWindows {
		kill, abort = sigKILL, sigKILL
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"Type", s.typ, coresvc.TypeSimple},
		{"TimeoutStartSec", s.timeoutStart, 90 * time.Second},
		{"TimeoutStopSec", s.timeoutStop, 90 * time.Second},
		{"TimeoutAbortSec", s.timeoutAbort, 90 * time.Second},
		{"RestartSec", s.restartDelay, 100 * time.Millisecond},
		{"RestartMaxDelaySec", s.restartMax, coresvc.Infinity},
		{"RuntimeMaxSec", s.runtimeMax, coresvc.Infinity},
		{"StartLimitIntervalSec", s.limitInterval, 10 * time.Second},
		{"StartLimitBurst", s.limitBurst, 5},
		{"KillSignal", s.killSignal, kill},
		{"RestartKillSignal", s.restartKillSignal, kill},
		{"FinalKillSignal", s.finalKillSignal, sigKILL},
		{"WatchdogSignal", s.watchdogSig, abort},
		{"ReloadSignal", s.reloadSignal, sigHUP},
		{"SendSIGKILL", s.on(setSendSIGKILL), true},
		{"KillMode", s.killMode, coresvc.KillModeProcess},
		{"NotifyAccess", s.notifyAccess, coresvc.NotifyAccessNone},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	o, err := resolve(&Config{Name: "u", Type: coresvc.TypeOneshot, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	if err != nil || o.timeoutStart != coresvc.Infinity {
		t.Fatalf("a oneshot's start timeout: %v %v", o.timeoutStart, err)
	}
	z, err := resolve(&Config{Name: "u", TimeoutStart: coresvc.Zero, StartLimitInterval: coresvc.Zero, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	if err != nil || z.timeoutStart != coresvc.Infinity || !z.on(setLimitOff) {
		t.Fatalf("systemd's 0: start %v limit off %v, %v", z.timeoutStart, z.on(setLimitOff), err)
	}
}

// TestPrivilegedControl: "+" and "!" are PRIVILEGE_REFUSED unless allowed.
func TestPrivilegedControl(t *testing.T) {
	for _, prefix := range []string{"+", "!"} {
		cfg := Config{
			Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
			ExecStartPre: []coresvc.CommandValue{cmd(t, prefix+"/bin/prepare")},
		}
		_, err := resolve(&cfg)
		want := coresvc.CodePrivilegeRefused
		if runtime.GOOS == osWindows {
			want = coresvc.CodeDirectiveUnsupported
		}
		if !hasCode(err, want) || field(err, "directive") != "ExecStartPre" {
			t.Errorf("%s without AllowPrivilegedControl: %v", prefix, err)
		}
		cfg.AllowPrivilegedControl = true
		if _, err := resolve(&cfg); (err == nil) != (runtime.GOOS != osWindows) {
			t.Errorf("%s allowed: %v", prefix, err)
		}
	}
}

// TestPrefixes: each prefix's effect on the spawned spec — "@" argv[0], ":"
// no expansion, "+" and "!" no credentials, the fixed path and no loader
// variable, "|" the shell; "-" is the reducer's (TestExecChain).
func TestPrefixes(t *testing.T) {
	s := &settings{shell: "/bin/sh"}
	tmpl := coreproc.Spec{
		User: "svc", Group: "svc", Groups: []string{"a"},
		Env: []string{"LD_PRELOAD=/x.so", "ENV=/e", "BASH_ENV=/b", "PATH=/opt/bin", "WHO=world"},
	}
	spec := func(words ...string) coreproc.Spec {
		return buildSpec(&tmpl, s, &effect{cmd: cmd(t, words...), vars: []string{"MAINPID=42"}})
	}
	if got := spec("@/bin/d", "daemon", "-x").Args; !slices.Equal(got, []string{"daemon", "-x"}) {
		t.Errorf("@: %q", got)
	}
	if got := spec("/bin/echo", "${WHO}", "$MAINPID", "$$").Args; !slices.Equal(got, []string{"/bin/echo", "world", "42", "$"}) {
		t.Errorf("expanded: %q", got)
	}
	if got := spec(":/bin/echo", "${WHO}", "$$").Args; !slices.Equal(got, []string{"/bin/echo", "${WHO}", "$$"}) {
		t.Errorf(":: %q", got)
	}
	for _, p := range []string{"+", "!"} {
		got := spec(p + "/bin/fix")
		if got.User != "" || got.Group != "" || got.Groups != nil {
			t.Errorf("%s kept credentials: %+v", p, got)
		}
		for _, v := range got.Env {
			if strings.HasPrefix(v, "LD_") || strings.HasPrefix(v, "ENV=") || strings.HasPrefix(v, "BASH_ENV=") {
				t.Errorf("%s kept %s", p, v)
			}
		}
		if !slices.Contains(got.Env, "PATH="+fixedPath) || slices.Contains(got.Env, "PATH=/opt/bin") {
			t.Errorf("%s PATH: %v", p, got.Env)
		}
	}
	if got := spec("/bin/d"); got.User != "svc" || !slices.Contains(got.Env, "LD_PRELOAD=/x.so") {
		t.Errorf("an unprefixed command lost its template: %+v", got)
	}
	sh := spec("|echo", "a b", "it's")
	if sh.Path != "/bin/sh" || !slices.Equal(sh.Args, []string{"/bin/sh", "-c", `echo 'a b' 'it'\''s'`}) {
		t.Errorf("|: %q %q", sh.Path, sh.Args)
	}
	if login := spec("@|"); !slices.Equal(login.Args, []string{"-sh"}) {
		t.Errorf("@|: %q", login.Args)
	}
	if !slices.Equal(tmpl.Env, []string{"LD_PRELOAD=/x.so", "ENV=/e", "BASH_ENV=/b", "PATH=/opt/bin", "WHO=world"}) {
		t.Errorf("the template was written to: %v", tmpl.Env)
	}
}

// TestExpandArgs is systemd.service(5)'s own example: ONE=one, TWO='two two'
// too, THREE= gives "one", "'two two' too", "" with ${…}, and "one", "two
// two", "too" with $….
func TestExpandArgs(t *testing.T) {
	env := []string{"ONE=one", "TWO='two two' too", "THREE="}
	if got := expandArgs([]string{"/bin/echo", "${ONE}", "${TWO}", "${THREE}"}, env); !slices.Equal(got, []string{"/bin/echo", "one", "'two two' too", ""}) {
		t.Errorf("${}: %q", got)
	}
	if got := expandArgs([]string{"/bin/echo", "$ONE", "$TWO", "$THREE"}, env); !slices.Equal(got, []string{"/bin/echo", "one", "two two", "too"}) {
		t.Errorf("$: %q", got)
	}
	if got := mergeEnv([]string{"A=1", "A=2", "MAINPID=9"}, []string{"MAINPID=42"}); !slices.Equal(got, []string{"A=2", "PATH=" + fixedPath, "MAINPID=42"}) {
		t.Errorf("mergeEnv: %q", got)
	}
}

// TestMergeEnvKeepsTheLast: a repeated name of the template keeps its last
// value, at the place of its last occurrence, as os/exec reads
// append(os.Environ(), "K=v") and as proc/exec hands a child; a name the
// engine sets replaces every occurrence, whatever its place.
func TestMergeEnvKeepsTheLast(t *testing.T) {
	type tc struct {
		name       string
		base, vars []string
		want       []string
	}
	tests := []tc{
		{"the last value, at its place", []string{"A=1", "B=b", "A=2"}, nil, []string{"B=b", "A=2", "PATH=" + fixedPath}},
		{"the engine's name replaces every occurrence", []string{"N=1", "K=k", "N=2"}, []string{"N=3"}, []string{"K=k", "PATH=" + fixedPath, "N=3"}},
		{"a repeated PATH keeps its last", []string{"PATH=/a", "PATH=/b"}, nil, []string{"PATH=/b"}},
		{"an empty PATH is a PATH", []string{"PATH="}, nil, []string{"PATH="}},
		{"the engine's PATH wins over the template's", []string{"PATH=/a"}, []string{"PATH=/b"}, []string{"PATH=/b"}},
		{"nothing set", nil, nil, []string{"PATH=" + fixedPath}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := mergeEnv(c.base, c.vars); !slices.Equal(got, c.want) {
			t.Errorf("mergeEnv(%q, %q) = %q, want %q", c.base, c.vars, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
