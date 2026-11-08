package service

import (
	"path"
	"strings"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// needsUnit is what a refused cgroup mode's error names as what lifts it.
const needsUnit string = "a Cgroup that is a coresvc.CgroupUnit (cgroup v2, S0g #734)"

// resolveCgroup resolves the directives that run on the unit's control group
// (S0g, kitsunium/sdk#734): KillMode= — control-group by default with a
// CgroupUnit, process without —, ExitType=, Delegate=, OOMPolicy=, and the
// group every process of the unit is spawned into. Without a CgroupUnit each
// of them but its default is refused, naming the directive.
func resolveCgroup(cfg *Config, s *settings) error {
	unit, isUnit := cfg.Cgroup.(coresvc.CgroupUnit)
	s.put(setGroupUnit, isUnit)
	s.put(setDelegate, cfg.Delegate)
	s.exitType = cfg.ExitType
	s.killMode = cfg.KillMode
	//: systemd's default where a group can run it, process where none can.
	if s.killMode == coresvc.KillModeDefault {
		s.killMode = coresvc.KillModeProcess
		//: the group's.
		if isUnit {
			s.killMode = coresvc.KillModeControlGroup
		}
	}
	s.oomPolicy = cfg.OOMPolicy
	//: stop, or continue under Delegate= (systemd.service(5)).
	if s.oomPolicy == coresvc.OOMPolicyDefault {
		s.oomPolicy = coresvc.OOMPolicyStop
		//: a delegated unit handles its own.
		if cfg.Delegate {
			s.oomPolicy = coresvc.OOMPolicyContinue
		}
	}
	//: no group to run them on.
	if !isUnit {
		//: the first directive that needs one, refused.
		return withoutUnit(cfg)
	}
	//: the group every process is spawned into.
	return resolveCgroupPath(cfg, s, unit.Path())
}

// withoutUnit refuses the first directive set that needs a CgroupUnit.
func withoutUnit(cfg *Config) error {
	needs := []struct {
		on        bool
		directive string
		value     string
	}{
		{cfg.KillMode == coresvc.KillModeControlGroup, "KillMode", "control-group"},
		{cfg.KillMode == coresvc.KillModeMixed, "KillMode", "mixed"},
		{cfg.ExitType == coresvc.ExitTypeCgroup, "ExitType", "cgroup"},
		{cfg.Delegate, "Delegate", "yes"},
		{cfg.OOMPolicy != coresvc.OOMPolicyDefault, "OOMPolicy", cfg.OOMPolicy.String()},
	}
	//: each directive in turn.
	for _, n := range needs {
		//: set, with nothing to run it on.
		if n.on {
			//: DIRECTIVE_UNSUPPORTED naming it.
			return unsupported(n.directive, n.value, needsUnit)
		}
	}
	//: none of them.
	return nil
}

// resolveCgroupPath makes the unit's group the CgroupPath of every spawn
// whose template names none; one the template names must lie within the
// group — a group the service is placed in beneath it, as systemd's
// DelegateSubgroup= — or a stop would signal and watch a group the processes
// are not in.
func resolveCgroupPath(cfg *Config, s *settings, dir string) error {
	named := cfg.Spec.CgroupPath
	//: none named: the unit's.
	if named == "" {
		s.cgroupPath = dir
		//: the group's directory.
		return nil
	}
	//: a group with no directory to compare (a test's double), or one within.
	if dir == "" || withinGroup(dir, named) {
		//: the template's.
		return nil
	}
	//: CONFIG_INVALID naming it.
	return invalid("CgroupPath", named)
}

// withinGroup reports whether path is the group dir or a group beneath it.
func withinGroup(dir, named string) bool {
	dir, named = path.Clean(dir), path.Clean(named)
	//: the group itself, or below it.
	return named == dir || strings.HasPrefix(named, strings.TrimSuffix(dir, "/")+"/")
}
