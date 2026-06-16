//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc .

// Package proc is the capability-preflight facade for the process-supervision
// domain. Some proc primitives have no native, secure mechanic on every OS
// (cgroup is Linux/Windows-only, rlimit/sd_notify are Unix-only, …); the SDK
// honours those gaps by returning the typed [UnsupportedPlatform] error and
// NEVER panicking on its own. This package lets a CONSUMER instead choose to
// fail fast at its own startup — the idiomatic Go MustX pattern
// ([regexp.MustCompile] / [text/template.Must]): the library offers the panic,
// it never imposes it.
//
// # Why preflight instead of recover
//
// A process supervisor (a PID1) cannot rely on a recover() in its hot path: an
// unrecovered panic in pid 1 orphans every supervised service, and a panic that
// crosses a CGO/FFI boundary is undefined behaviour. The safe pattern is to
// reject an unsupported configuration up front — assert the required
// capabilities once, at startup, then route only to capabilities that
// [Supported] reports present.
//
//	func main() {
//		// crash-at-launch, clearly, if this host cannot supervise at all:
//		proc.MustSupport(proc.CapProcessSpawn, proc.CapReaper)
//		os.Exit(run())
//	}
//
//	// elsewhere, branch instead of crash for an optional capability:
//	if proc.Supported(proc.CapCgroup) {
//		// confine via cgroup / Job Object
//	}
//
// # Capability × platform matrix
//
//	Capability           linux darwin windows freebsd openbsd netbsd dragonfly
//	CapProcessSpawn        ✅     ✅     ✅      ✅      ✅      ✅      ✅
//	CapSignalRelay         ✅     ✅     ✅      ✅      ✅      ✅      ✅
//	CapReaper              ✅     ✅     ✅¹     ✅      ✅      ✅      ✅
//	CapCgroup              ✅     ❌     ✅²     ❌      ❌      ❌      ❌
//	CapRlimit              ✅     ✅     ❌³     ✅      ✅      ✅      ✅
//	CapUmaskNiceOOM        ✅     ✅     ❌      ✅      ✅      ✅      ✅
//	CapSdNotify            ✅     ✅     ❌      ✅      ✅      ✅      ✅
//	CapSocketActivation    ✅     ✅     ❌      ✅      ✅      ✅      ✅
//
// ¹ Windows has no zombies; the reaper is a correct no-op there. ² Windows
// cgroup is the Job Object backend. ³ the Unix setrlimit-on-self model has no
// Windows analogue; Windows resource limits are applied through the cgroup
// (Job Object) container or at spawn via the process Spec, not a standalone rlimit.
//
// [Supported] consults runtime.GOOS only — it is the platform-level matrix, not
// a runtime probe. A capability that is native on the GOOS may still be
// unavailable at runtime (e.g. cgroup present but not delegated); use the
// per-facade probe for that (e.g. cgroup.Available()).
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
	// CapUmaskNiceOOM is the spawn-time umask / nice / oom_score_adj knobs.
	CapUmaskNiceOOM
	// CapCgroup is a kernel-enforced resource container (cgroup.Create).
	CapCgroup
	// CapReaper is zombie reaping / subreaping (reaper.New).
	CapReaper
	// CapSignalRelay is forwarding signals to a pid / process group (signal.Relay).
	CapSignalRelay
	// CapSdNotify is the service-manager readiness/watchdog protocol (sdnotify).
	CapSdNotify
	// CapSocketActivation is inheriting a pre-opened listening socket (sdlisten).
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
	unixTargets = []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly"}
	// unixAndWindows adds Windows to the Unix set (spawn / signal / reaper).
	unixAndWindows = []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly", "windows"}

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
