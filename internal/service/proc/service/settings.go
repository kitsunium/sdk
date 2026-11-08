package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// defaultTimeout is DefaultTimeoutStartSec= and DefaultTimeoutStopSec=.
	defaultTimeout time.Duration = 90 * time.Second
	// defaultRestartDelay is RestartSec='s default.
	defaultRestartDelay time.Duration = 100 * time.Millisecond
	// defaultLimitInterval is DefaultStartLimitIntervalSec=.
	defaultLimitInterval time.Duration = 10 * time.Second
	// defaultLimitBurst is DefaultStartLimitBurst=.
	defaultLimitBurst int = 5
	// defaultShell runs a "|" command when Config.Shell is empty.
	defaultShell string = "/bin/sh"
	// maxExitCode is the largest exit status.
	maxExitCode int = 255
	// osWindows is the GOOS the signal rules differ on.
	osWindows string = "windows"
	// osLinux is the GOOS whose notify socket carries descriptors.
	osLinux string = "linux"
)

// list names an Exec*= list; the reducer runs one at a time.
type list uint8

const (
	// listNone is no list: no command runs.
	listNone list = iota
	// listCondition is ExecCondition=.
	listCondition
	// listStartPre is ExecStartPre=.
	listStartPre
	// listStart is ExecStart=, the main process.
	listStart
	// listStartPost is ExecStartPost=.
	listStartPost
	// listReload is ExecReload=.
	listReload
	// listReloadPost is ExecReloadPost=.
	listReloadPost
	// listStop is ExecStop=.
	listStop
	// listStopPost is ExecStopPost=.
	listStopPost
	// listCount is how many lists there are.
	listCount
)

// settingFlag is one of the settings' booleans, as a bit.
type settingFlag uint16

const (
	// setRemainAfterExit is RemainAfterExit=.
	setRemainAfterExit settingFlag = 1 << iota
	// setSendSIGHUP is SendSIGHUP=.
	setSendSIGHUP
	// setSendSIGKILL is SendSIGKILL=.
	setSendSIGKILL
	// setSurviveFinal is SurviveFinalKillSignal=.
	setSurviveFinal
	// setHasCgroup is a unit with a CgroupKill.
	setHasCgroup
	// setHasAttacher is a unit with an Attacher.
	setHasAttacher
	// setLimitOff is StartLimitIntervalSec=0: no limiter.
	setLimitOff
	// setGuess is GuessMainPID= on a Type=forking unit without PIDFile=.
	setGuess
	// setInstance is a unit of one connection (S0f): it ends with its run.
	setInstance
	// setGroupUnit is a unit whose Cgroup is a CgroupUnit: the cgroup modes
	// run (S0g).
	setGroupUnit
	// setDelegate is Delegate=.
	setDelegate
	// setSandboxed is a unit whose main process runs in a sandbox (S2): its
	// uid 0 is no privilege.
	setSandboxed
	// setRootStartOnly is RootDirectoryStartOnly= with a root: ExecStart=
	// alone pivots into it.
	setRootStartOnly
)

// listDirectives names each list's directive, for errors.
var listDirectives = [listCount]string{"", "ExecCondition", "ExecStartPre", "ExecStart", "ExecStartPost", "ExecReload", "ExecReloadPost", "ExecStop", "ExecStopPost"}

// settings is a Config resolved and checked: every default applied, every
// duration either finite or coresvc.Infinity. The reducer reads it and
// nothing else of the configuration; it never changes once New returned.
// sandbox is every command's sandbox — the template's with the directives'
// —, sandboxAway the one of every command but ExecStart='s under
// RootDirectoryStartOnly=: plain values, built once, never written to.
type settings struct {
	sandbox, sandboxAway          *coreproc.SandboxValue
	lists                         [listCount][]coresvc.CommandValue
	success, prevent, force       coresvc.ExitStatusValue
	name, socket, shell, pidFile  string
	storeKey, cgroupPath, busName string
	restartDelay, restartMax      time.Duration
	restartJitter                 time.Duration
	timeoutStart, timeoutStop     time.Duration
	timeoutAbort, runtimeMax      time.Duration
	runtimeJitter, watchdog       time.Duration
	limitInterval                 time.Duration
	limitBurst, restartSteps      int
	storeMax                      int
	selfPID                       int
	killSignal, restartKillSignal coreproc.Signal
	finalKillSignal, watchdogSig  coreproc.Signal
	reloadSignal, sigcont, sighup coreproc.Signal
	typ                           coresvc.Type
	restart                       coresvc.Restart
	restartMode                   coresvc.RestartMode
	startMode, stopMode           coresvc.TimeoutMode
	notifyAccess                  coresvc.NotifyAccess
	killMode                      coresvc.KillMode
	exitType                      coresvc.ExitType
	oomPolicy                     coresvc.OOMPolicy
	preserve                      coresvc.Preserve
	flags                         settingFlag
}

// on reports whether flag f is set.
func (s *settings) on(f settingFlag) bool {
	//: the bit.
	return s.flags&f != 0
}

// put sets flag f when on is true.
func (s *settings) put(f settingFlag, on bool) {
	//: set.
	if on {
		s.flags |= f
	}
}

// resolve checks cfg and resolves its defaults.
func resolve(cfg *Config) (*settings, error) {
	//: a unit is named.
	if cfg.Name == "" {
		//: CONFIG_INVALID naming the field.
		return nil, invalid("Name", "")
	}
	//: a value no constant names.
	if err := checkRanges(cfg); err != nil {
		//: CONFIG_INVALID.
		return nil, err
	}
	s := &settings{name: cfg.Name, selfPID: os.Getpid()}
	s.put(setHasCgroup, cfg.Cgroup != nil)
	s.put(setHasAttacher, cfg.Attacher != nil)
	steps := []func(*Config, *settings) error{
		resolveType, resolveBus, resolveCgroup, resolveForking, resolveCommands, resolveSandbox, resolveRestart,
		resolveDurations, resolveSignals, resolveStore, resolveNotify, resolveLimits,
	}
	//: each concern in turn; the first refusal wins.
	for _, step := range steps {
		//: a refusal names the directive.
		if err := step(cfg, s); err != nil {
			//: CONFIG_INVALID, DIRECTIVE_UNSUPPORTED or PRIVILEGE_REFUSED.
			return nil, err
		}
	}
	//: the unit's settings.
	return s, nil
}

// checkRanges refuses an enumeration value no constant names.
func checkRanges(cfg *Config) error {
	ranges := []struct {
		directive string
		value     int
		maxValue  int
	}{
		{"Type", int(cfg.Type), int(coresvc.TypeIdle)},
		{"Restart", int(cfg.Restart), int(coresvc.RestartAlways)},
		{"RestartMode", int(cfg.RestartMode), int(coresvc.RestartModeDebug)},
		{"TimeoutStartFailureMode", int(cfg.TimeoutStartFailureMode), int(coresvc.TimeoutKill)},
		{"TimeoutStopFailureMode", int(cfg.TimeoutStopFailureMode), int(coresvc.TimeoutKill)},
		{"NotifyAccess", int(cfg.NotifyAccess), int(coresvc.NotifyAccessAll)},
		{"KillMode", int(cfg.KillMode), int(coresvc.KillModeNone)},
		{"ExitType", int(cfg.ExitType), int(coresvc.ExitTypeCgroup)},
		{"OOMPolicy", int(cfg.OOMPolicy), int(coresvc.OOMPolicyKill)},
		{"FileDescriptorStorePreserve", int(cfg.FileDescriptorStorePreserve), int(coresvc.PreserveOnSuccess)},
		{"SendSIGKILL", int(cfg.SendSIGKILL), int(coresvc.ToggleNo)},
		{"GuessMainPID", int(cfg.GuessMainPID), int(coresvc.ToggleNo)},
	}
	//: each enumeration against its last constant.
	for _, r := range ranges {
		//: a value past the last constant.
		if r.value > r.maxValue {
			//: CONFIG_INVALID naming it.
			return invalid(r.directive, strconv.Itoa(r.value))
		}
	}
	//: every enumeration names a constant.
	return nil
}

// resolveType resolves Type= and the flags that follow from it.
func resolveType(cfg *Config, s *settings) error {
	s.typ = cfg.Type
	//: systemd's default: dbus with a BusName=, simple with an ExecStart=,
	//: oneshot without.
	if s.typ == coresvc.TypeDefault {
		s.typ = defaultType(cfg)
	}
	s.put(setRemainAfterExit, cfg.RemainAfterExit)
	s.put(setSendSIGHUP, cfg.SendSIGHUP)
	s.put(setSendSIGKILL, cfg.SendSIGKILL.On(true))
	s.put(setSurviveFinal, cfg.SurviveFinalKillSignal)
	s.shell = cfg.Shell
	//: the shell a "|" command runs through.
	if s.shell == "" {
		s.shell = defaultShell
	}
	//: a type that waits for a notification needs a socket to hear it on.
	if isNotifyType(s.typ) && runtime.GOOS == osWindows {
		//: no sd_notify socket on Windows.
		return unsupported("Type", s.typ.String(), "windows")
	}
	//: the type's.
	return nil
}

// resolveForking resolves PIDFile= and GuessMainPID=, which only Type=forking
// reads: its main process is a daemon the engine adopts, so it needs an
// Attacher, and a PID file or a cgroup to find the daemon in.
func resolveForking(cfg *Config, s *settings) error {
	//: every other type ignores both, as systemd does.
	if s.typ != coresvc.TypeForking {
		//: nothing to resolve.
		return nil
	}
	//: Windows has no fork to adopt the result of.
	if runtime.GOOS == osWindows {
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("Type", "forking", "windows")
	}
	//: the daemon is not the engine's child: only adopted can it be watched.
	if cfg.Attacher == nil {
		//: CONFIG_INVALID naming the port.
		return invalid("Attacher", "none for Type=forking")
	}
	//: a PID file, read through the Attacher (S3).
	if cfg.PIDFile != "" {
		//: AttachPIDFile walks an absolute clean path only.
		if !filepath.IsAbs(cfg.PIDFile) || filepath.Clean(cfg.PIDFile) != cfg.PIDFile {
			//: CONFIG_INVALID naming it.
			return invalid("PIDFile", cfg.PIDFile)
		}
		s.pidFile = cfg.PIDFile
		//: the PID file names the main process.
		return nil
	}
	//: without one, the cgroup must say who is left, guessed or not.
	if _, lists := cfg.Cgroup.(coresvc.CgroupMembers); !lists {
		//: DIRECTIVE_UNSUPPORTED: the main process could never be found.
		return unsupported("GuessMainPID", strconv.FormatBool(cfg.GuessMainPID.On(true)), "PIDFile= or a Cgroup that lists its members (S0g #734)")
	}
	s.put(setGuess, cfg.GuessMainPID.On(true))
	//: the cgroup names it.
	return nil
}

// resolveCommands checks the Exec*= lists.
func resolveCommands(cfg *Config, s *settings) error {
	s.lists = [listCount][]coresvc.CommandValue{
		listCondition: cfg.ExecCondition, listStartPre: cfg.ExecStartPre, listStart: cfg.ExecStart,
		listStartPost: cfg.ExecStartPost, listReload: cfg.ExecReload, listReloadPost: cfg.ExecReloadPost,
		listStop: cfg.ExecStop, listStopPost: cfg.ExecStopPost,
	}
	//: exactly one main command, except for a oneshot.
	if s.typ != coresvc.TypeOneshot && len(cfg.ExecStart) != 1 {
		//: CONFIG_INVALID naming ExecStart.
		return invalid("ExecStart", strconv.Itoa(len(cfg.ExecStart)))
	}
	//: a oneshot with no command must remain and be stopped.
	if len(cfg.ExecStart) == 0 && (!cfg.RemainAfterExit || len(cfg.ExecStop) == 0) {
		//: systemd refuses a service with neither.
		return invalid("ExecStart", "0")
	}
	//: every command of every list.
	for l := listCondition; l < listCount; l++ {
		//: one command at a time.
		for _, cmd := range s.lists[l] {
			//: a command the engine can run.
			if err := checkCommand(cfg, l, cmd); err != nil {
				//: refused, naming its directive.
				return err
			}
		}
	}
	//: every command.
	return nil
}

// checkCommand checks one command of a list.
func checkCommand(cfg *Config, l list, cmd coresvc.CommandValue) error {
	//: a program, or the shell alone.
	if cmd.Path == "" && cmd.Flags&coresvc.FlagShell == 0 {
		//: CONFIG_INVALID naming the list.
		return invalid(listDirectives[l], "")
	}
	//: a program with no argument vector.
	if cmd.Path != "" && len(cmd.Args) == 0 {
		//: CONFIG_INVALID naming the list: ParseCommand always fills Args.
		return invalid(listDirectives[l], cmd.Path)
	}
	//: "+" and "!" only where the caller allows them.
	if cmd.Flags.Privileged() {
		//: no elevation on Windows.
		if runtime.GOOS == osWindows {
			//: DIRECTIVE_UNSUPPORTED naming the list.
			return unsupported(listDirectives[l], cmd.Flags.String(), "windows")
		}
		//: refused unless allowed.
		if !cfg.AllowPrivilegedControl {
			//: PRIVILEGE_REFUSED naming the list.
			return errs.Wrap(coresvc.PrivilegeRefused, errs.WrapParams{}, errs.String("directive", listDirectives[l]), errs.String("unit", cfg.Name))
		}
	}
	//: a runnable command.
	return nil
}

// resolveRestart resolves Restart= and the exit status sets.
func resolveRestart(cfg *Config, s *settings) error {
	s.restart, s.restartMode, s.restartSteps = cfg.Restart, cfg.RestartMode, cfg.RestartSteps
	//: a oneshot never restarts on a clean end.
	if s.typ == coresvc.TypeOneshot && (s.restart == coresvc.RestartAlways || s.restart == coresvc.RestartOnSuccess) {
		//: systemd refuses it.
		return invalid("Restart", s.restart.String())
	}
	//: a negative step count.
	if cfg.RestartSteps < 0 {
		//: CONFIG_INVALID.
		return invalid("RestartSteps", strconv.Itoa(cfg.RestartSteps))
	}
	sets := []struct {
		set       coresvc.ExitStatusValue
		directive string
	}{
		{cfg.SuccessExitStatus, "SuccessExitStatus"},
		{cfg.RestartPreventExitStatus, "RestartPreventExitStatus"},
		{cfg.RestartForceExitStatus, "RestartForceExitStatus"},
	}
	//: every set.
	for _, e := range sets {
		//: an exit status in 0..255, a signal the platform knows.
		if err := checkStatusSet(e.set, e.directive); err != nil {
			//: CONFIG_INVALID naming it.
			return err
		}
	}
	s.success, s.prevent, s.force = cfg.SuccessExitStatus, cfg.RestartPreventExitStatus, cfg.RestartForceExitStatus
	//: the restart policy.
	return nil
}

// checkStatusSet refuses an exit code out of 0..255 or an unknown signal.
func checkStatusSet(set coresvc.ExitStatusValue, directive string) error {
	//: every code.
	for _, c := range set.Codes {
		//: out of the exit status range.
		if c < 0 || c > maxExitCode {
			//: CONFIG_INVALID.
			return invalid(directive, strconv.Itoa(c))
		}
	}
	//: every signal.
	for _, sig := range set.Signals {
		//: a signal the platform does not name.
		if !sig.Known() {
			//: CONFIG_INVALID.
			return invalid(directive, sig.String())
		}
	}
	//: a valid set.
	return nil
}

// resolveDurations resolves every duration directive.
func resolveDurations(cfg *Config, s *settings) error {
	oneshotStart := defaultTimeout
	//: a oneshot has no start timeout by default.
	if s.typ == coresvc.TypeOneshot {
		oneshotStart = coresvc.Infinity
	}
	durations := []struct {
		dst       *time.Duration
		directive string
		value     time.Duration
		def       time.Duration
		zero      time.Duration
	}{
		{&s.timeoutStart, "TimeoutStartSec", cfg.TimeoutStart, oneshotStart, coresvc.Infinity},
		{&s.timeoutStop, "TimeoutStopSec", cfg.TimeoutStop, defaultTimeout, coresvc.Infinity},
		{&s.runtimeMax, "RuntimeMaxSec", cfg.RuntimeMax, coresvc.Infinity, coresvc.Infinity},
		{&s.runtimeJitter, "RuntimeRandomizedExtraSec", cfg.RuntimeRandomizedExtra, 0, 0},
		{&s.watchdog, "WatchdogSec", cfg.Watchdog, 0, 0},
		{&s.restartDelay, "RestartSec", cfg.RestartDelay, defaultRestartDelay, 0},
		{&s.restartMax, "RestartMaxDelaySec", cfg.RestartMaxDelay, coresvc.Infinity, coresvc.Infinity},
		{&s.restartJitter, "RestartRandomizedDelaySec", cfg.RestartRandomizedDelay, 0, 0},
	}
	//: each duration: the default, systemd's 0, infinity or a finite value.
	for _, d := range durations {
		v, err := resolveDuration(d.directive, d.value, d.def, d.zero)
		//: a negative duration other than Zero.
		if err != nil {
			//: CONFIG_INVALID.
			return err
		}
		*d.dst = v
	}
	abort, err := resolveDuration("TimeoutAbortSec", cfg.TimeoutAbort, s.timeoutStop, coresvc.Infinity)
	//: a negative duration other than Zero.
	if err != nil {
		//: CONFIG_INVALID.
		return err
	}
	s.timeoutAbort = abort
	s.startMode, s.stopMode = cfg.TimeoutStartFailureMode, cfg.TimeoutStopFailureMode
	//: no watchdog on Windows, which has no sd_notify socket.
	if s.watchdog > 0 && s.watchdog != coresvc.Infinity && runtime.GOOS == osWindows {
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("WatchdogSec", cfg.Watchdog.String(), "windows")
	}
	//: a watchdog of infinity is none.
	if s.watchdog == coresvc.Infinity {
		s.watchdog = 0
	}
	//: the durations.
	return nil
}

// resolveDuration reads one duration directive: zero is def, coresvc.Zero is
// zero, coresvc.Infinity is infinity, a negative value is refused.
func resolveDuration(directive string, value, def, zero time.Duration) (time.Duration, error) {
	//: the directive's default, systemd's literal 0, or the caller's value.
	switch {
	//: the Go zero asks for the default.
	case value == 0:
		//: systemd's default.
		return def, nil
	//: systemd's literal 0.
	case value == coresvc.Zero:
		//: what 0 means for this directive.
		return zero, nil
	//: a negative duration is no duration.
	case value < 0:
		//: CONFIG_INVALID.
		return 0, invalid(directive, value.String())
	//: a finite value, or Infinity.
	default:
		//: the caller's.
		return value, nil
	}
}

// resolveSignals resolves the kill, watchdog and reload signals.
func resolveSignals(cfg *Config, s *settings) error {
	s.sigcont, s.sighup = platformSignals()
	kill, abort := sigTERM, sigABRT
	//: Windows kills or does nothing.
	if runtime.GOOS == osWindows {
		kill, abort = sigKILL, sigKILL
	}
	signals := []struct {
		dst       *coreproc.Signal
		directive string
		value     coreproc.Signal
		def       coreproc.Signal
	}{
		{&s.killSignal, "KillSignal", cfg.KillSignal, kill},
		{&s.finalKillSignal, "FinalKillSignal", cfg.FinalKillSignal, sigKILL},
		{&s.watchdogSig, "WatchdogSignal", cfg.WatchdogSignal, abort},
		{&s.reloadSignal, "ReloadSignal", cfg.ReloadSignal, sigHUP},
	}
	//: each signal: its default, or one the platform knows.
	for _, sig := range signals {
		v, err := resolveSignal(sig.directive, sig.value, sig.def)
		//: an unknown or, on Windows, an undeliverable signal.
		if err != nil {
			//: refused, naming it.
			return err
		}
		*sig.dst = v
	}
	restartKill, err := resolveSignal("RestartKillSignal", cfg.RestartKillSignal, s.killSignal)
	//: an unknown or undeliverable signal.
	if err != nil {
		//: refused, naming it.
		return err
	}
	s.restartKillSignal = restartKill
	//: no SIGHUP to send on Windows.
	if s.on(setSendSIGHUP) && runtime.GOOS == osWindows {
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("SendSIGHUP", "yes", "windows")
	}
	//: the signals.
	return nil
}

// resolveSignal reads one signal directive.
func resolveSignal(directive string, value, def coreproc.Signal) (coreproc.Signal, error) {
	//: the zero is the default.
	if value == 0 {
		//: systemd's default for the platform.
		return def, nil
	}
	//: a signal the platform does not name.
	if !value.Known() {
		//: CONFIG_INVALID.
		return 0, invalid(directive, strconv.Itoa(value.Int()))
	}
	//: Windows delivers a kill and nothing else.
	if runtime.GOOS == osWindows && value != sigKILL {
		//: DIRECTIVE_UNSUPPORTED.
		return 0, unsupported(directive, value.String(), "windows")
	}
	//: the caller's.
	return value, nil
}

// resolveNotify resolves NotifyAccess= and the socket.
func resolveNotify(cfg *Config, s *settings) error {
	s.notifyAccess = cfg.NotifyAccess
	//: a notify type, a watchdog and a store imply main.
	if s.notifyAccess == coresvc.NotifyAccessNone && listensToMain(s) {
		s.notifyAccess = coresvc.NotifyAccessMain
	}
	//: exec and all need to know who belongs to the unit.
	if (s.notifyAccess == coresvc.NotifyAccessExec || s.notifyAccess == coresvc.NotifyAccessAll) && cfg.Cgroup == nil {
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("NotifyAccess", s.notifyAccess.String(), "no cgroup")
	}
	//: reading a message needs a socket.
	if s.notifyAccess != coresvc.NotifyAccessNone && cfg.Notify == nil {
		//: CONFIG_INVALID: the unit could never be heard.
		return invalid("NotifyAccess", s.notifyAccess.String()+" without a NotifySource")
	}
	//: the socket the processes are told.
	if cfg.Notify != nil {
		s.socket = cfg.Notify.Socket()
	}
	//: the notification settings.
	return nil
}

// resolveStore resolves FileDescriptorStoreMax= and
// FileDescriptorStorePreserve=: a store is fed by FDSTORE=1 through the
// notification socket, which carries descriptors on Linux alone.
func resolveStore(cfg *Config, s *settings) error {
	n := cfg.FileDescriptorStoreMax
	//: a negative maximum.
	if n < 0 {
		//: CONFIG_INVALID.
		return invalid("FileDescriptorStoreMax", strconv.Itoa(n))
	}
	//: no store: FileDescriptorStorePreserve= has nothing to keep.
	if n == 0 {
		//: none.
		return nil
	}
	//: the store's watch, duplicate test and ids are Linux's (fdstore_linux.go).
	if runtime.GOOS != osLinux {
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("FileDescriptorStoreMax", strconv.Itoa(n), runtime.GOOS)
	}
	//: nothing could ever fill it.
	if cfg.Notify == nil {
		//: CONFIG_INVALID naming it.
		return invalid("FileDescriptorStoreMax", strconv.Itoa(n)+" without a NotifySource")
	}
	s.storeMax, s.preserve, s.storeKey = n, cfg.FileDescriptorStorePreserve, storeKey(cfg, s.sandbox)
	//: the store.
	return nil
}

// storeKey is the key a store is filled under: the unit's name and a digest
// of what its main process is — every ExecStart= command, the user, the
// group and the groups it runs as, and the sandbox it runs in (sb, the
// template's with the directives'; nil for none). A store whose key the unit
// no longer matches is closed whole, never handed over.
func storeKey(cfg *Config, sb *coreproc.SandboxValue) string {
	var b strings.Builder
	field := func(v string) {
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
	}
	field(sandboxKey(sb))
	field(cfg.Spec.User)
	field(cfg.Spec.Group)
	field(strconv.Itoa(len(cfg.Spec.Groups)))
	//: each supplementary group.
	for _, g := range cfg.Spec.Groups {
		field(g)
	}
	field(strconv.Itoa(len(cfg.ExecStart)))
	//: each command: its program, its prefixes, its words.
	for _, c := range cfg.ExecStart {
		field(c.Path)
		field(strconv.Itoa(int(c.Flags)))
		field(strconv.Itoa(len(c.Args)))
		//: each word.
		for _, w := range c.Args {
			field(w)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	//: the name, then the digest.
	return cfg.Name + "/" + hex.EncodeToString(sum[:])
}

// resolveLimits resolves the start limiter.
func resolveLimits(cfg *Config, s *settings) error {
	//: a negative burst.
	if cfg.StartLimitBurst < 0 {
		//: CONFIG_INVALID.
		return invalid("StartLimitBurst", strconv.Itoa(cfg.StartLimitBurst))
	}
	s.limitBurst = cfg.StartLimitBurst
	//: the default burst.
	if s.limitBurst == 0 {
		s.limitBurst = defaultLimitBurst
	}
	interval, err := resolveDuration("StartLimitIntervalSec", cfg.StartLimitInterval, defaultLimitInterval, 0)
	//: a negative interval other than Zero.
	if err != nil {
		//: CONFIG_INVALID.
		return err
	}
	s.limitInterval = interval
	s.put(setLimitOff, interval == 0)
	//: the limiter.
	return nil
}

// listensToMain reports whether a unit must hear its main process: a notify
// type, a watchdog, a descriptor store.
func listensToMain(s *settings) bool {
	//: any of the three.
	return isNotifyType(s.typ) || s.watchdog > 0 || s.storeMax > 0
}

// isNotifyType reports whether t waits for READY=1.
func isNotifyType(t coresvc.Type) bool {
	//: notify and notify-reload.
	return t == coresvc.TypeNotify || t == coresvc.TypeNotifyReload
}

// invalid is CONFIG_INVALID naming a directive and its value.
func invalid(directive, value string) error {
	//: the core sentinel with the directive.
	return errs.Wrap(coresvc.ConfigInvalid, errs.WrapParams{}, errs.String("directive", directive), errs.String("value", value))
}

// unsupported is DIRECTIVE_UNSUPPORTED naming a directive, its value and what
// lifts the refusal.
func unsupported(directive, value, issue string) error {
	//: the core sentinel with the directive.
	return errs.Wrap(coresvc.DirectiveUnsupported, errs.WrapParams{},
		errs.String("directive", directive), errs.String("value", value), errs.String("issue", issue))
}
