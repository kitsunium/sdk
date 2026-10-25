package net

import "time"

// OverrideValue reports what the operator's environment did to the engines,
// read once, before any probe (ADR 0188). The zero value is "no override".
type OverrideValue struct {
	// Source is "environment" when KITSUNIUM_NET_ENGINE or KITSUNIUM_NET_DISABLE
	// is set to a non-empty value, and empty otherwise.
	Source string `json:"source,omitempty"`
	// Raw is what was read, each variable set as NAME=value, separated by a
	// space, in the order EnvEngine, EnvDisable.
	Raw string `json:"raw,omitempty"`
	// Applied is the highest engine the environment lets a group run on, after
	// both variables; empty when nothing is set. A group runs on
	// min(its code's choice, Applied), skipping what Disabled names.
	Applied Capability `json:"applied,omitempty"`
	// Disabled lists the engines KITSUNIUM_NET_DISABLE switched off.
	Disabled []Capability `json:"disabled,omitempty"`
}

// FaultValue reports one thing the server runs without, or cannot do, now: a
// degraded listener or loop group, a drained ring, a stalled loop group, or an
// environment token the registry does not know (ADR 0188).
type FaultValue struct {
	// Source says what reported it: "listener", "loop", "ring", "stall" or
	// "environment".
	Source string `json:"source"`
	// Group names the group concerned; empty for the environment.
	Group string `json:"group,omitempty"`
	// Reason names the fault and, where one exists, the runbook's switch.
	Reason string `json:"reason"`
}

// FallbacksValue counts the times a group started on a lower engine than the one
// its code asked for, because the better one failed, and keeps the last one
// (ADR 0188). An override by the environment is not a fallback.
type FallbacksValue struct {
	// Count is the number of fallbacks since the server was built.
	Count uint64 `json:"count"`
	// LastGroup names the group of the last fallback.
	LastGroup string `json:"last_group,omitempty"`
	// LastReason is the last fallback's reason.
	LastReason string `json:"last_reason,omitempty"`
	// LastAt is when the last fallback happened; zero when none did.
	LastAt time.Time `json:"last_at"`
}

// PlatformValue names what the server runs on, so a report says which kernel a
// probe ran against (ADR 0188).
type PlatformValue struct {
	// GOOS is runtime.GOOS.
	GOOS string `json:"goos"`
	// GOARCH is runtime.GOARCH.
	GOARCH string `json:"goarch"`
	// Kernel is the operating system's release as the platform reports it: the
	// uname release on Linux, kern.osrelease on darwin and the BSDs,
	// major.minor.build as GetVersion reports it on Windows; empty when it cannot
	// be read, and on illumos and Solaris, where the standard library reads none.
	Kernel string `json:"kernel,omitempty"`
	// Go is runtime.Version(), the toolchain the binary was built with.
	Go string `json:"go"`
}
