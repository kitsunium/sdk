package proc

import (
	"runtime"
	"slices"
	"strconv"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// maxPublicLen is the wire-safe Public ceiling (ADR 0005: ≤120 runes); the panic
// message falls back to a capability-less form when naming them would exceed it.
const maxPublicLen int = 120

// Capability identifies one process-supervision capability whose native backend
// is present on some platforms and absent on others.
type Capability int

// The supervisable capabilities, one per platform-sensitive proc primitive.
const (
	// CapProcessSpawn is spawning + supervising a child process (process.Start).
	CapProcessSpawn Capability = iota
	// CapRlimit is applying per-process setrlimit-style limits (rlimit.Apply).
	CapRlimit
	// CapUmaskNice is the spawn-time umask and nice knobs (Spec.Umask, Spec.Nice),
	// native on every Unix. The OOM-killer bias is CapOOMScoreAdj.
	CapUmaskNice
	// CapCgroup is a kernel-enforced resource container (cgroup.Create).
	CapCgroup
	// CapReaper is zombie reaping / subreaping (reaper.New).
	CapReaper
	// CapSignalRelay is forwarding signals to a pid / process group (signal.Relay).
	CapSignalRelay
	// CapSdNotify is the service-manager readiness/watchdog protocol (systemd/notify).
	CapSdNotify
	// CapSocketActivation is inheriting a pre-opened listening socket (systemd/listen).
	CapSocketActivation
	// CapDieWithParent is binding a child to the supervisor's life (Spec.DieWithParent).
	CapDieWithParent
	// CapScheduling is a child's CPU policy and I/O class (Spec.Scheduling).
	CapScheduling
	// CapPTY is spawning a child on a pseudo-terminal (StdioPTY).
	CapPTY
	// CapProcessHandle is reaching a process through a kernel handle, not its pid (ProcessHandle, process.Open).
	CapProcessHandle
)

// CapOOMScoreAdj is the spawn-time OOM-killer bias (Spec.OOMScoreAdj), a Linux
// procfs knob: elsewhere Start refuses it with UnsupportedPlatform before
// spawning anything. It is numbered 12, apart from the block above: 8 to 11 are
// the spawn's own capabilities (ADR 0217), and a value is never given twice.
const CapOOMScoreAdj Capability = 12

// CapSandbox is spawning a child under Spec.Sandbox: namespaces, mounts,
// capabilities, Landlock and seccomp, applied before its program runs. Linux on
// amd64 and arm64 alone, where the system call tables are written; every other
// GOOS and GOARCH refuses it with UnsupportedPlatform before spawning anything.
// It is numbered 13, after CapOOMScoreAdj.
const CapSandbox Capability = 13

var (
	// capabilityNames maps each capability to its stable diagnostic name, indexed
	// by the Capability value so String stays a flat O(1) lookup. A value no
	// capability holds is an empty entry.
	capabilityNames = [...]string{
		CapProcessSpawn:     "ProcessSpawn",
		CapRlimit:           "Rlimit",
		CapUmaskNice:        "UmaskNice",
		CapOOMScoreAdj:      "OOMScoreAdj",
		CapCgroup:           "Cgroup",
		CapReaper:           "Reaper",
		CapSignalRelay:      "SignalRelay",
		CapSdNotify:         "SdNotify",
		CapSocketActivation: "SocketActivation",
		CapDieWithParent:    "DieWithParent",
		CapScheduling:       "Scheduling",
		CapPTY:              "PTY",
		CapProcessHandle:    "ProcessHandle",
		CapSandbox:          "Sandbox",
	}

	// unixTargets is the SDK's Unix GOOS set (the capabilities gated to Unix).
	// illumos is listed apart from solaris because runtime.GOOS names it apart,
	// even though the solaris build tag also selects it.
	unixTargets = []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly", "illumos", "solaris"}
	// unixAndWindows adds Windows to the Unix set (spawn / signal / reaper).
	unixAndWindows = []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly", "illumos", "solaris", "windows"}

	// capabilityGOOS lists the GOOS on which each capability has a native backend;
	// a capability absent from this map is unsupported everywhere.
	capabilityGOOS = map[Capability][]string{
		CapProcessSpawn:     unixAndWindows,
		CapSignalRelay:      unixAndWindows,
		CapReaper:           unixAndWindows,
		CapCgroup:           {"linux", "windows", "freebsd"},
		CapRlimit:           unixTargets,
		CapUmaskNice:        unixTargets,
		CapOOMScoreAdj:      {"linux"},
		CapSdNotify:         unixTargets,
		CapSocketActivation: unixTargets,
		CapDieWithParent:    {"linux", "freebsd", "windows"},
		CapScheduling:       {"linux", "freebsd"},
		CapPTY:              {"linux", "darwin", "freebsd"},
		CapProcessHandle:    {"linux", "windows"},
		CapSandbox:          sandboxGOOS(runtime.GOARCH),
	}

	// UnsupportedPlatform is the central sentinel a missing capability surfaces. A
	// recover() of a [MustSupport] panic yields an error carrying this error's code.
	UnsupportedPlatform = coreproc.UnsupportedPlatform
)

// String returns the capability's stable name, or its integer form when the
// value is out of range.
func (c Capability) String() string {
	//: a value no capability holds renders with its integer for debuggability.
	if i := int(c); i < 0 || i >= len(capabilityNames) || capabilityNames[i] == "" {
		//: unknown capability → its numeric form.
		return "Capability(" + strconv.Itoa(int(c)) + ")"
	}
	//: the stable name for a known capability.
	return capabilityNames[c]
}

// Supported reports whether cap has a native backend on the current GOOS. It is
// pure, allocation-free and race-clean: it consults runtime.GOOS only, never a
// runtime delegation probe (see the package doc on cgroup.Available()).
func Supported(c Capability) bool {
	goss, ok := capabilityGOOS[c]
	//: a capability absent from the table has no native backend anywhere.
	if !ok {
		//: an unrecognised capability is conservatively unsupported.
		return false
	}
	//: present when the current GOOS is in the capability's native set.
	return slices.Contains(goss, runtime.GOOS)
}

// sandboxGOOS is CapSandbox's row for the architecture arch the binary is built
// for: Linux on amd64 and arm64, whose system call tables the sandbox's seccomp
// filter is written against, and no GOOS on any other architecture.
func sandboxGOOS(arch string) []string {
	//: the tabulated architectures.
	if arch == "amd64" || arch == "arm64" {
		//: Linux alone.
		return []string{"linux"}
	}
	//: refused everywhere else.
	return nil
}

// MissingCapabilities returns the subset of caps with no native backend on the
// current platform; an empty (nil) result means every capability is present.
func MissingCapabilities(caps ...Capability) []Capability {
	//: accumulate only the unsupported capabilities; nil when all are present.
	var missing []Capability
	//: test each requested capability against the platform matrix.
	for _, c := range caps {
		//: collect the ones the current GOOS cannot honour natively.
		if !Supported(c) {
			//: record the gap for the caller (and for MustSupport's message).
			missing = append(missing, c)
		}
	}
	//: the gaps, or nil when the platform supports every requested capability.
	return missing
}

// MustSupport panics when any requested capability is missing on the current
// platform, and is a no-op otherwise. It is the consumer's OPT-IN fail-fast: the
// SDK never calls it. The panic value is the typed [errs] UnsupportedPlatform
// error (code 0.2.6.1), so a top-level recover() can classify it via
// errs.CodeOf / HasCode rather than a bare string.
func MustSupport(caps ...Capability) {
	//: collect the gaps; an empty set is the supported fast path.
	missing := MissingCapabilities(caps...)
	//: every requested capability is present — nothing to fail on.
	if len(missing) == 0 {
		//: no-op success.
		return
	}
	//: the consumer opted into crash-at-launch — panic with the typed error.
	panic(unsupportedError(missing))
}

// unsupportedError builds the typed UnsupportedPlatform error carried by a
// MustSupport panic: it reuses the central code 0.2.6.1 so a recover() classifies
// it identically to the fallible paths, and names the missing capabilities (the
// wire-safe Public falls back to a capability-less form past the 120-rune ceiling).
func unsupportedError(missing []Capability) error {
	//: render the missing capability names for the diagnostic detail.
	names := make([]string, len(missing))
	//: stringify each gap in order.
	for i, c := range missing {
		//: stable capability name.
		names[i] = c.String()
	}
	list := strings.Join(names, ",")
	platform := runtime.GOOS + "/" + runtime.GOARCH
	public := "process capability [" + list + "] unsupported on " + platform
	//: keep Public within the wire-safe ceiling; drop the names if they overflow.
	if len(public) > maxPublicLen {
		//: a capability-less message still names the platform and stays bounded.
		public = "required process capability unsupported on " + platform
	}
	//: reuse the central UNSUPPORTED_PLATFORM code via the public errs constructor
	//: (runtime-validated, never panics); Private + the field carry the full list.
	return errs.New(coreproc.CodeUnsupportedPlatform, "UNSUPPORTED_PLATFORM",
		public,
		"proc.MustSupport: missing ["+list+"] on "+platform,
		errs.String("missing", list), errs.String("platform", platform))
}
