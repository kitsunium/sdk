package service

import "strconv"

var (
	// typeNames are the Type= spellings.
	typeNames = [...]string{"", "simple", "exec", "forking", "oneshot", "dbus", "notify", "notify-reload", "idle"}
	// restartNames are the Restart= spellings.
	restartNames = [...]string{"no", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort", "always"}
	// restartModeNames are the RestartMode= spellings.
	restartModeNames = [...]string{"normal", "direct", "debug"}
	// killModeNames are the KillMode= spellings.
	killModeNames = [...]string{"", "process", "control-group", "mixed", "none"}
	// exitTypeNames are the ExitType= spellings.
	exitTypeNames = [...]string{"main", "cgroup"}
	// notifyAccessNames are the NotifyAccess= spellings.
	notifyAccessNames = [...]string{"none", "main", "exec", "all"}
	// timeoutModeNames are the failure modes' spellings.
	timeoutModeNames = [...]string{"terminate", "abort", "kill"}
	// oomPolicyNames are the OOMPolicy= spellings.
	oomPolicyNames = [...]string{"", "continue", "stop", "kill"}
	// preserveNames are the FileDescriptorStorePreserve= spellings.
	preserveNames = [...]string{"restart", "no", "yes", "on-success"}
	// stateNames are the states' spellings.
	stateNames = [...]string{"inactive", "activating", "active", "reloading", "deactivating", "failed", "auto-restart", "exited", "condition-skipped"}
	// activeStates are systemd's ActiveState of each State.
	activeStates = [...]string{"inactive", "activating", "active", "reloading", "deactivating", "failed", "activating", "active", "inactive"}
	// subNames are systemd's SubState spellings.
	subNames = [...]string{
		"dead", "condition", "start-pre", "start", "start-post", "running", "exited",
		"reload", "reload-signal", "reload-notify", "reload-post", "stop", "stop-watchdog",
		"stop-sigterm", "stop-sigkill", "stop-post", "final-watchdog", "final-sigterm",
		"final-sigkill", "failed", "auto-restart", "running-revalidating",
	}
	// resultNames are systemd's Result= spellings.
	resultNames = [...]string{
		"success", "resources", "protocol", "timeout", "exit-code", "signal",
		"core-dump", "watchdog", "start-limit-hit", "oom-kill", "exec-condition",
	}
)

// String returns the Type= spelling ("notify-reload"), "" for TypeDefault, or
// "type(N)" for a value no constant names.
func (t Type) String() string {
	//: a defined type has its systemd spelling.
	if int(t) < len(typeNames) {
		//: the table's entry.
		return typeNames[t]
	}
	//: an undefined value stays legible.
	return "type(" + strconv.Itoa(int(t)) + ")"
}

// String returns the Restart= spelling ("on-abnormal"), or "restart(N)".
func (r Restart) String() string {
	//: a defined policy has its systemd spelling.
	if int(r) < len(restartNames) {
		//: the table's entry.
		return restartNames[r]
	}
	//: an undefined value stays legible.
	return "restart(" + strconv.Itoa(int(r)) + ")"
}

// String returns the RestartMode= spelling, or "restartmode(N)".
func (m RestartMode) String() string {
	//: a defined mode has its systemd spelling.
	if int(m) < len(restartModeNames) {
		//: the table's entry.
		return restartModeNames[m]
	}
	//: an undefined value stays legible.
	return "restartmode(" + strconv.Itoa(int(m)) + ")"
}

// String returns the KillMode= spelling, "" for KillModeDefault, or
// "killmode(N)".
func (m KillMode) String() string {
	//: a defined mode has its systemd spelling.
	if int(m) < len(killModeNames) {
		//: the table's entry.
		return killModeNames[m]
	}
	//: an undefined value stays legible.
	return "killmode(" + strconv.Itoa(int(m)) + ")"
}

// String returns the ExitType= spelling, or "exittype(N)".
func (e ExitType) String() string {
	//: a defined type has its systemd spelling.
	if int(e) < len(exitTypeNames) {
		//: the table's entry.
		return exitTypeNames[e]
	}
	//: an undefined value stays legible.
	return "exittype(" + strconv.Itoa(int(e)) + ")"
}

// String returns the NotifyAccess= spelling, or "notifyaccess(N)".
func (a NotifyAccess) String() string {
	//: a defined access has its systemd spelling.
	if int(a) < len(notifyAccessNames) {
		//: the table's entry.
		return notifyAccessNames[a]
	}
	//: an undefined value stays legible.
	return "notifyaccess(" + strconv.Itoa(int(a)) + ")"
}

// String returns the failure mode's spelling, or "timeoutmode(N)".
func (m TimeoutMode) String() string {
	//: a defined mode has its systemd spelling.
	if int(m) < len(timeoutModeNames) {
		//: the table's entry.
		return timeoutModeNames[m]
	}
	//: an undefined value stays legible.
	return "timeoutmode(" + strconv.Itoa(int(m)) + ")"
}

// String returns the OOMPolicy= spelling, "" for the default, or
// "oompolicy(N)".
func (p OOMPolicy) String() string {
	//: a defined policy has its systemd spelling.
	if int(p) < len(oomPolicyNames) {
		//: the table's entry.
		return oomPolicyNames[p]
	}
	//: an undefined value stays legible.
	return "oompolicy(" + strconv.Itoa(int(p)) + ")"
}

// String returns the FileDescriptorStorePreserve= spelling, or "preserve(N)".
func (p Preserve) String() string {
	//: a defined value has its systemd spelling.
	if int(p) < len(preserveNames) {
		//: the table's entry.
		return preserveNames[p]
	}
	//: an undefined value stays legible.
	return "preserve(" + strconv.Itoa(int(p)) + ")"
}

// On reports whether the toggle is yes, def standing for ToggleDefault.
func (t Toggle) On(def bool) bool {
	//: the default is the directive's.
	if t == ToggleDefault {
		//: the caller's default.
		return def
	}
	//: an explicit choice.
	return t == ToggleYes
}

// String returns the state's spelling ("auto-restart"), or "state(N)".
func (s State) String() string {
	//: a defined state has its spelling.
	if int(s) < len(stateNames) {
		//: the table's entry.
		return stateNames[s]
	}
	//: an undefined value stays legible.
	return "state(" + strconv.Itoa(int(s)) + ")"
}

// ActiveState returns systemd's ActiveState for s: StateAutoRestart is
// "activating", StateExited "active", StateConditionSkipped "inactive".
func (s State) ActiveState() string {
	//: a defined state has its ActiveState.
	if int(s) < len(activeStates) {
		//: the table's entry.
		return activeStates[s]
	}
	//: an undefined value stays legible.
	return s.String()
}

// String returns systemd's SubState spelling ("stop-sigterm"), or "sub(N)".
func (s Sub) String() string {
	//: a defined step has its systemd spelling.
	if int(s) < len(subNames) {
		//: the table's entry.
		return subNames[s]
	}
	//: an undefined value stays legible.
	return "sub(" + strconv.Itoa(int(s)) + ")"
}

// String returns systemd's Result= spelling ("exit-code"), or "result(N)".
func (r Result) String() string {
	//: a defined result has its systemd spelling.
	if int(r) < len(resultNames) {
		//: the table's entry.
		return resultNames[r]
	}
	//: an undefined value stays legible.
	return "result(" + strconv.Itoa(int(r)) + ")"
}
