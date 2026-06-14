// Package proc — the Spec value type: an immutable process spawn specification.
package proc

// Spec is the immutable description of a process to spawn: the executable and
// its environment, the credentials and isolation topology to apply, and the
// resource/scheduling attributes to set between fork and exec. It carries no
// policy — restart, health, and dependency concerns belong to the caller.
//
// Pointer fields (Umask, Nice, OOMScoreAdj) distinguish "leave at the inherited
// default" (nil) from "set explicitly to zero". Maps and slices are read by the
// service layer and never mutated.
type Spec struct {
	// Path is the absolute or PATH-resolvable executable to run. Required.
	Path string
	// Args is the full argv including argv[0]; when empty the service uses
	// [Path] as the sole argument.
	Args []string
	// Dir is the working directory; empty means inherit the parent's.
	Dir string
	// Env is the explicit environment; nil yields an empty environment, never
	// implicit inheritance, so a spawned service starts from a known state.
	Env []string

	// User is the name or numeric uid to setuid to; empty leaves the parent's.
	User string
	// Group is the name or numeric gid to setgid to; empty leaves the parent's.
	Group string
	// Groups are supplementary group names or gids (systemd SupplementaryGroups=).
	Groups []string

	// Setpgid places the child in its own process group, enabling group-kill of
	// the whole tree (systemd KillMode=control-group without cgroups).
	Setpgid bool
	// Setsid starts the child in a new session detached from the controlling tty.
	Setsid bool

	// Umask, when non-nil, is the file-creation mask applied to the child
	// (systemd UMask=).
	Umask *int
	// Nice, when non-nil, is the scheduling-priority adjustment applied to the
	// child (systemd Nice=).
	Nice *int
	// OOMScoreAdj, when non-nil, is the /proc/<pid>/oom_score_adj value applied
	// to the child (systemd OOMScoreAdjust=).
	OOMScoreAdj *int
	// Rlimits are per-resource soft/hard limits applied post-fork, pre-exec
	// (systemd Limit*=); resources unsupported on the platform surface a typed
	// error rather than silently no-op.
	Rlimits map[Resource]LimitValue
}
