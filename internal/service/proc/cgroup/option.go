// Package cgroup — the Option functional-option type and its accumulator.
package cgroup

// mountRoot is the canonical mount point of the unified cgroup v2 hierarchy.
// Exposed as the default root so Option can override it for tests against a
// delegated sub-tree.
const mountRoot string = "/sys/fs/cgroup"

// groupConfig accumulates the options applied to a Create call. It is
// unexported (no public ROLE suffix needed) and built only via the Option
// closures so callers cannot construct an inconsistent value.
type groupConfig struct {
	// root is the cgroup v2 directory under which the new group is created.
	root string
}

// Option customises a Create call (functional-option pattern). Construct
// options with the WithRoot helper; the zero set yields a group directly under
// the unified cgroup v2 mount.
type Option func(*groupConfig)

// defaultConfig returns the baseline groupConfig used when no Option overrides
// it: the new group is created directly under the unified cgroup v2 mount.
func defaultConfig() groupConfig {
	//: the canonical mount is the default parent for a new control group.
	return groupConfig{root: mountRoot}
}

// applyOptions folds opts onto a fresh default config and returns the result.
func applyOptions(opts []Option) groupConfig {
	cfg := defaultConfig()
	//: each closure mutates the accumulator in declaration order.
	for _, opt := range opts {
		//: a nil option is a no-op rather than a panic, mirroring stdlib options.
		if opt != nil {
			//: apply the override to the accumulator.
			opt(&cfg)
		}
	}
	//: the fully-folded config drives Create.
	return cfg
}

// WithRoot overrides the parent directory under which Create makes the new
// control group. Use it to target a delegated sub-tree (e.g. the cgroup a
// container manager handed to the caller) instead of the top-level mount.
func WithRoot(root string) Option {
	//: capture root in a closure that sets it on the accumulator.
	return func(c *groupConfig) {
		//: override the parent directory for the new group.
		c.root = root
	}
}
