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

// The supervisable capabilities, one per platform-sensitive proc primitive.
const (
	// CapProcessSpawn is spawning + supervising a child process (process.Start).
	CapProcessSpawn Capability = iota
	// CapRlimit is applying per-process setrlimit-style limits (rlimit.Apply).
	CapRlimit
	// CapUmaskNiceOOM is the spawn-time umask / nice / oom_score_adj knobs.
	CapUmaskNiceOOM
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
)

var (
	// capabilityNames maps each capability to its stable diagnostic name, indexed
	// by the Capability value so String stays a flat O(1) lookup.
	capabilityNames = [...]string{
		CapProcessSpawn:     "ProcessSpawn",
		CapRlimit:           "Rlimit",
		CapUmaskNiceOOM:     "UmaskNiceOOM",
		CapCgroup:           "Cgroup",
		CapReaper:           "Reaper",
		CapSignalRelay:      "SignalRelay",
		CapSdNotify:         "SdNotify",
		CapSocketActivation: "SocketActivation",
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
		CapCgroup:           {"linux", "windows"},
		CapRlimit:           unixTargets,
		CapUmaskNiceOOM:     unixTargets,
		CapSdNotify:         unixTargets,
		CapSocketActivation: unixTargets,
	}

	// UnsupportedPlatform is the central sentinel a missing capability surfaces. A
	// recover() of a [MustSupport] panic yields an error carrying this error's code.
	UnsupportedPlatform = coreproc.UnsupportedPlatform
)

// String returns the capability's stable name, or its integer form when the
// value is out of range.
func (c Capability) String() string {
	//: an out-of-range value renders with its integer for debuggability.
	if i := int(c); i < 0 || i >= len(capabilityNames) {
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
