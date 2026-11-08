package bpf

import (
	"math"
	"net/netip"
	"path"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Validate refuses a device policy no program can be built from, with
// POLICY_INVALID naming the rule and the field: a default or a verdict that
// is neither Allow nor Deny, more than MaxDeviceRules rules, a type out of
// its enumeration, an empty access set or one with an unknown bit, a number
// that is neither AnyNumber nor an unsigned 32-bit value.
func (p DevicePolicyValue) Validate() error {
	//: the default first: it decides every request the rules leave.
	if !p.Default.valid() {
		//: no default verdict.
		return policyInvalid(-1, "default")
	}
	//: the rules are compiled in a straight line, so their number is bounded.
	if len(p.Rules) > MaxDeviceRules {
		//: too many rules.
		return policyInvalid(len(p.Rules), "rules")
	}
	//: each rule on its own.
	for i, r := range p.Rules {
		//: the first field a rule gets wrong is the one named.
		if field := r.invalidField(); field != "" {
			//: the rule and the field.
			return policyInvalid(i, field)
		}
	}
	//: a policy a program can be built from.
	return nil
}

// invalidField names the first field of a device rule that is out of its
// range, "" when none is.
func (r DeviceRuleValue) invalidField() string {
	//: one check per field, in declaration order.
	switch {
	case r.Type < DeviceBlock || r.Type > DeviceAny:
		//: an unknown type.
		return "type"
	case !validNumber(r.Major):
		//: a major number out of range.
		return "major"
	case !validNumber(r.Minor):
		//: a minor number out of range.
		return "minor"
	case r.Access == 0 || r.Access&^AccessAll != 0:
		//: an access set that matches nothing or names an unknown access.
		return "access"
	case !r.Verdict.valid():
		//: no verdict.
		return "verdict"
	default:
		//: a valid rule.
		return ""
	}
}

// Validate refuses an IP policy no pair of programs can be built from, with
// POLICY_INVALID naming the rule and the field: a default or a verdict that
// is neither Allow nor Deny, more than MaxPrefixRules rules, a prefix that
// is not valid or is an IPv4-mapped IPv6 one, a prefix given twice.
func (p SKBPolicyValue) Validate() error {
	//: the default first: it decides every address no prefix holds.
	if !p.Default.valid() {
		//: no default verdict.
		return policyInvalid(-1, "default")
	}
	//: the maps are sized from the rules, so their number is bounded.
	if len(p.Rules) > MaxPrefixRules {
		//: too many rules.
		return policyInvalid(len(p.Rules), "rules")
	}
	//: a prefix twice would leave the maps to decide which verdict wins.
	seen := make(map[netip.Prefix]struct{}, len(p.Rules))
	//: each rule on its own, then against the ones before it.
	for i, r := range p.Rules {
		//: an invalid prefix, or one the wire never carries.
		if !r.Prefix.IsValid() || r.Prefix.Addr().Is4In6() {
			//: the prefix.
			return policyInvalid(i, "prefix")
		}
		//: no verdict.
		if !r.Verdict.valid() {
			//: the verdict.
			return policyInvalid(i, "verdict")
		}
		key := r.Prefix.Masked()
		//: the same prefix, however it was spelled, is given once.
		if _, dup := seen[key]; dup {
			//: a duplicate.
			return policyInvalid(i, "duplicate")
		}
		seen[key] = struct{}{}
	}
	//: a policy a pair of programs can be built from.
	return nil
}

// Validate refuses a bind policy no pair of programs can be built from, with
// POLICY_INVALID naming the rule and the field: a default or a verdict that
// is neither Allow nor Deny, more than MaxBindRules rules, a family or a
// transport out of its enumeration, a range whose First is past its Last.
func (p BindPolicyValue) Validate() error {
	//: the default first: it decides every bind the rules leave.
	if !p.Default.valid() {
		//: no default verdict.
		return policyInvalid(-1, "default")
	}
	//: the rules are compiled in a straight line, so their number is bounded.
	if len(p.Rules) > MaxBindRules {
		//: too many rules.
		return policyInvalid(len(p.Rules), "rules")
	}
	//: each rule on its own.
	for i, r := range p.Rules {
		//: the first field a rule gets wrong is the one named.
		if field := r.invalidField(); field != "" {
			//: the rule and the field.
			return policyInvalid(i, field)
		}
	}
	//: a policy a pair of programs can be built from.
	return nil
}

// invalidField names the first field of a bind rule that is out of its
// range, "" when none is.
func (r BindRuleValue) invalidField() string {
	//: one check per field, in declaration order.
	switch {
	case r.Family < FamilyIPv4 || r.Family > FamilyAny:
		//: an unknown family.
		return "family"
	case r.Transport < TransportTCP || r.Transport > TransportAny:
		//: an unknown transport.
		return "transport"
	case r.Ports.First > r.Ports.Last:
		//: an inverted range.
		return "ports"
	case !r.Verdict.valid():
		//: no verdict.
		return "verdict"
	default:
		//: a valid rule.
		return ""
	}
}

// CheckCgroup refuses, with POLICY_INVALID, a control group a program cannot
// be attached to by name: a path that is not absolute, not clean, or the
// root of the filesystem. It does not look at the filesystem: the backend
// opens the directory and refuses one that is not a cgroup v2 directory.
func CheckCgroup(cgroup string) error {
	//: an absolute, clean path below the root: what GroupTree.Path returns.
	if !strings.HasPrefix(cgroup, "/") || path.Clean(cgroup) != cgroup || cgroup == "/" {
		//: the path, as a field for the log.
		return errs.Wrap(PolicyInvalid, errs.WrapParams{}, errs.String("field", "cgroup"), errs.String("cgroup", cgroup))
	}
	//: a path the backend may open.
	return nil
}

// valid reports whether v is Allow or Deny.
func (v Verdict) valid() bool {
	//: the two verdicts, nothing else.
	return v == Allow || v == Deny
}

// validNumber reports whether n is AnyNumber or an unsigned 32-bit value.
func validNumber(n int64) bool {
	//: the wildcard, or what the kernel's u32 holds.
	return n == AnyNumber || (n >= 0 && n <= math.MaxUint32)
}

// policyInvalid is POLICY_INVALID naming the rule — -1 for the policy
// itself, the count for too many rules — and the field.
func policyInvalid(rule int, field string) error {
	//: the core sentinel, the rule and the field as fields.
	return errs.Wrap(PolicyInvalid, errs.WrapParams{}, errs.Int("rule", rule), errs.String("field", field))
}
