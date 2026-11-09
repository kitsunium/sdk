package bpf_test

import (
	"math"
	"net/netip"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	"github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// fieldOf returns the "field" a POLICY_INVALID names, "" for another error.
func fieldOf(err error) string {
	//: only the core's refusal names a field.
	if !errs.HasCode(err, bpf.CodePolicyInvalid) {
		return ""
	}
	//: the field.
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "field" {
			return f.StringValue()
		}
	}
	return ""
}

// device is a valid device rule the cases below spoil one field of.
var device = bpf.DeviceRuleValue{Type: bpf.DeviceChar, Major: 1, Minor: 3, Access: bpf.AccessRead, Verdict: bpf.Allow}

// TestDevicePolicyValidateNamesTheField refuses each field out of its
// range, and accepts the wildcards and the u32 edges.
func TestDevicePolicyValidateNamesTheField(t *testing.T) {
	with := func(f func(*bpf.DeviceRuleValue)) bpf.DevicePolicyValue {
		r := device
		f(&r)
		return bpf.DevicePolicyValue{Default: bpf.Deny, Rules: []bpf.DeviceRuleValue{r}}
	}
	cases := map[string]struct {
		policy bpf.DevicePolicyValue
		field  string
	}{
		"zero":           {bpf.DevicePolicyValue{}, "default"},
		"too-many":       {bpf.DevicePolicyValue{Default: bpf.Deny, Rules: make([]bpf.DeviceRuleValue, bpf.MaxDeviceRules+1)}, "rules"},
		"type-zero":      {with(func(r *bpf.DeviceRuleValue) { r.Type = 0 }), "type"},
		"type-past":      {with(func(r *bpf.DeviceRuleValue) { r.Type = bpf.DeviceAny + 1 }), "type"},
		"major-negative": {with(func(r *bpf.DeviceRuleValue) { r.Major = -2 }), "major"},
		"minor-past-u32": {with(func(r *bpf.DeviceRuleValue) { r.Minor = math.MaxUint32 + 1 }), "minor"},
		"access-empty":   {with(func(r *bpf.DeviceRuleValue) { r.Access = 0 }), "access"},
		"access-unknown": {with(func(r *bpf.DeviceRuleValue) { r.Access = 8 }), "access"},
		"verdict-zero":   {with(func(r *bpf.DeviceRuleValue) { r.Verdict = 0 }), "verdict"},
		"verdict-past":   {with(func(r *bpf.DeviceRuleValue) { r.Verdict = bpf.Allow + 1 }), "verdict"},
		"valid":          {with(func(*bpf.DeviceRuleValue) {}), ""},
		"wildcards":      {with(func(r *bpf.DeviceRuleValue) { r.Major, r.Minor, r.Type = bpf.AnyNumber, bpf.AnyNumber, bpf.DeviceAny }), ""},
		"u32-edges":      {with(func(r *bpf.DeviceRuleValue) { r.Major, r.Minor, r.Access = 0, math.MaxUint32, bpf.AccessAll }), ""},
	}
	//: each case.
	for name, c := range cases {
		err := c.policy.Validate()
		//: refused on the field, or accepted.
		if got := fieldOf(err); got != c.field || (c.field == "") != (err == nil) {
			t.Errorf("%s: Validate = %v (field %q), want field %q", name, err, got, c.field)
		}
	}
}

// TestSKBPolicyValidateNamesTheField refuses an invalid prefix, an
// IPv4-mapped one, a duplicate however it is spelled, a
// missing verdict, and accepts both families.
func TestSKBPolicyValidateNamesTheField(t *testing.T) {
	rule := func(p string, v bpf.Verdict) bpf.PrefixRuleValue {
		return bpf.PrefixRuleValue{Prefix: netip.MustParsePrefix(p), Verdict: v}
	}
	cases := map[string]struct {
		policy bpf.SKBPolicyValue
		field  string
	}{
		"zero":       {bpf.SKBPolicyValue{}, "default"},
		"too-many":   {bpf.SKBPolicyValue{Default: bpf.Allow, Rules: make([]bpf.PrefixRuleValue, bpf.MaxPrefixRules+1)}, "rules"},
		"invalid":    {bpf.SKBPolicyValue{Default: bpf.Allow, Rules: []bpf.PrefixRuleValue{{Verdict: bpf.Deny}}}, "prefix"},
		"mapped":     {bpf.SKBPolicyValue{Default: bpf.Allow, Rules: []bpf.PrefixRuleValue{rule("::ffff:10.0.0.0/104", bpf.Deny)}}, "prefix"},
		"verdict":    {bpf.SKBPolicyValue{Default: bpf.Allow, Rules: []bpf.PrefixRuleValue{rule("10.0.0.0/8", 0)}}, "verdict"},
		"duplicate":  {bpf.SKBPolicyValue{Default: bpf.Allow, Rules: []bpf.PrefixRuleValue{rule("10.0.0.0/8", bpf.Deny), rule("10.1.2.3/8", bpf.Allow)}}, "duplicate"},
		"both":       {bpf.SKBPolicyValue{Default: bpf.Deny, Rules: []bpf.PrefixRuleValue{rule("10.0.0.0/8", bpf.Allow), rule("fd00::/8", bpf.Allow)}}, ""},
		"accounting": {bpf.SKBPolicyValue{Default: bpf.Allow}, ""},
	}
	//: each case.
	for name, c := range cases {
		err := c.policy.Validate()
		//: refused on the field, or accepted.
		if got := fieldOf(err); got != c.field || (c.field == "") != (err == nil) {
			t.Errorf("%s: Validate = %v (field %q), want field %q", name, err, got, c.field)
		}
	}
}

// TestBindPolicyValidateNamesTheField refuses each field out of its range.
func TestBindPolicyValidateNamesTheField(t *testing.T) {
	ok := bpf.BindRuleValue{Family: bpf.FamilyIPv4, Transport: bpf.TransportTCP, Ports: bpf.PortRangeValue{First: 1, Last: 1023}, Verdict: bpf.Deny}
	with := func(f func(*bpf.BindRuleValue)) bpf.BindPolicyValue {
		r := ok
		f(&r)
		return bpf.BindPolicyValue{Default: bpf.Allow, Rules: []bpf.BindRuleValue{r}}
	}
	cases := map[string]struct {
		policy bpf.BindPolicyValue
		field  string
	}{
		"zero":           {bpf.BindPolicyValue{}, "default"},
		"too-many":       {bpf.BindPolicyValue{Default: bpf.Allow, Rules: make([]bpf.BindRuleValue, bpf.MaxBindRules+1)}, "rules"},
		"family":         {with(func(r *bpf.BindRuleValue) { r.Family = 0 }), "family"},
		"transport":      {with(func(r *bpf.BindRuleValue) { r.Transport = bpf.TransportAny + 1 }), "transport"},
		"inverted-range": {with(func(r *bpf.BindRuleValue) { r.Ports = bpf.PortRangeValue{First: 2, Last: 1} }), "ports"},
		"verdict":        {with(func(r *bpf.BindRuleValue) { r.Verdict = 0 }), "verdict"},
		"valid":          {with(func(*bpf.BindRuleValue) {}), ""},
		"port-zero":      {with(func(r *bpf.BindRuleValue) { r.Ports = bpf.PortRangeValue{} }), ""},
	}
	//: each case.
	for name, c := range cases {
		err := c.policy.Validate()
		//: refused on the field, or accepted.
		if got := fieldOf(err); got != c.field || (c.field == "") != (err == nil) {
			t.Errorf("%s: Validate = %v (field %q), want field %q", name, err, got, c.field)
		}
	}
}

// TestCheckCgroupRefusesWhatCannotBeAGroupByName refuses a relative path,
// an unclean one, the filesystem's root and the empty path, and accepts
// what GroupTree.Path returns.
func TestCheckCgroupRefusesWhatCannotBeAGroupByName(t *testing.T) {
	cases := map[string]bool{
		"":                                false,
		"/":                               false,
		"sys/fs/cgroup/unit":              false,
		"/sys/fs/cgroup/../etc":           false,
		"/sys/fs/cgroup/unit/":            false,
		"/sys/fs/cgroup//unit":            false,
		"0::/system.slice/unit":           false,
		"/sys/fs/cgroup/system.slice/u.s": true,
	}
	//: each path.
	for path, want := range cases {
		err := bpf.CheckCgroup(path)
		//: accepted, or POLICY_INVALID on the cgroup field.
		if (err == nil) != want || (err != nil && fieldOf(err) != "cgroup") {
			t.Errorf("CheckCgroup(%q) = %v, want accepted=%v", path, err, want)
		}
	}
}
