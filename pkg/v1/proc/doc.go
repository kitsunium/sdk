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
//	Capability           linux darwin windows freebsd openbsd netbsd dragonfly illumos solaris
//	CapProcessSpawn        ✅     ✅     ✅      ✅      ✅      ✅      ✅        ✅      ✅
//	CapSignalRelay         ✅     ✅     ✅      ✅      ✅      ✅      ✅        ✅      ✅
//	CapReaper              ✅     ✅     ✅¹     ✅      ✅      ✅      ✅        ✅⁴     ✅⁴
//	CapCgroup              ✅     ❌     ✅²     ❌      ❌      ❌      ❌        ❌      ❌
//	CapRlimit              ✅     ✅     ❌³     ✅      ✅      ✅      ✅        ✅      ✅
//	CapUmaskNiceOOM        ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//	CapSdNotify            ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//	CapSocketActivation    ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//
// ¹ Windows has no zombies; the reaper is a correct no-op there. ² Windows
// cgroup is the Job Object backend. ³ the Unix setrlimit-on-self model has no
// Windows analogue; Windows resource limits are applied through the cgroup
// (Job Object) container or at spawn via the process Spec, not a standalone rlimit.
// ⁴ illumos and Solaris have no reparent-here facility, like darwin, OpenBSD
// and NetBSD: SetChildSubreaper returns [UnsupportedPlatform]. Their reaper
// also sweeps once a second, because the Go runtime forks every child there
// with FORK_NOSIGCHLD and its exit posts no SIGCHLD. Both columns are proven
// on OmniOS r151054 and Oracle Solaris 11.4 (amd64) by the SDK's
// cross-platform runtime lane.
//
// [Supported] consults runtime.GOOS only — it is the platform-level matrix, not
// a runtime probe. A capability that is native on the GOOS may still be
// unavailable at runtime (e.g. cgroup present but not delegated); use the
// per-facade probe for that (e.g. cgroup.Available()).
package proc
