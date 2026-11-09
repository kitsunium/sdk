package cgroup

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// The kernel's encoding of a request, written again here rather than read
// from the package, so the reference below is independent of the compiler.
const (
	reqBlock = 1
	reqChar  = 2
	reqMknod = 1
	reqRead  = 2
	reqWrite = 4
)

// deviceRequest is one call of a cgroup_device program.
type deviceRequest struct {
	typ, access  uint32
	major, minor uint32
}

// context is the request as struct bpf_cgroup_dev_ctx.
func (r deviceRequest) context() []byte {
	b := binary.NativeEndian.AppendUint32(nil, r.access<<16|r.typ)
	b = binary.NativeEndian.AppendUint32(b, r.major)
	return binary.NativeEndian.AppendUint32(b, r.minor)
}

// wantDevice is the policy's decision, read from its documentation: the
// first rule that matches decides; an Allow rule matches when it covers
// every requested access, a Deny rule when it covers one.
func wantDevice(p corebpf.DevicePolicyValue, r deviceRequest) bool {
	types := map[corebpf.DeviceType]uint32{corebpf.DeviceBlock: reqBlock, corebpf.DeviceChar: reqChar}
	//: the rules in order.
	for _, rule := range p.Rules {
		var set uint32
		//: the rule's accesses in the kernel's bits.
		for bit, kernel := range map[corebpf.DeviceAccess]uint32{corebpf.AccessMknod: reqMknod, corebpf.AccessRead: reqRead, corebpf.AccessWrite: reqWrite} {
			//: covered.
			if rule.Access&bit != 0 {
				set |= kernel
			}
		}
		typeOK := rule.Type == corebpf.DeviceAny || types[rule.Type] == r.typ
		accessOK := r.access&^set == 0
		//: a deny rule matches on one shared bit.
		if rule.Verdict == corebpf.Deny {
			accessOK = r.access&set != 0
		}
		majorOK := rule.Major == corebpf.AnyNumber || uint32(rule.Major) == r.major
		minorOK := rule.Minor == corebpf.AnyNumber || uint32(rule.Minor) == r.minor
		//: the first match decides.
		if typeOK && accessOK && majorOK && minorOK {
			return rule.Verdict == corebpf.Allow
		}
	}
	return p.Default == corebpf.Allow
}

// deviceRequests are the requests every policy is asked about: both
// types, numbers at the edges of an immediate and of a u32, each access
// alone and in pairs.
func deviceRequests() []deviceRequest {
	var out []deviceRequest
	//: the cross product.
	for _, typ := range []uint32{reqBlock, reqChar} {
		for _, access := range []uint32{reqMknod, reqRead, reqWrite, reqRead | reqWrite, reqMknod | reqRead, reqMknod | reqRead | reqWrite} {
			for _, major := range []uint32{0, 1, 5, 136, math.MaxInt32, math.MaxInt32 + 1, math.MaxUint32} {
				for _, minor := range []uint32{0, 3, 9, math.MaxUint32} {
					out = append(out, deviceRequest{typ: typ, access: access, major: major, minor: minor})
				}
			}
		}
	}
	return out
}

// checkDevicePolicy runs every request through the compiled policy and
// compares with wantDevice.
func checkDevicePolicy(t *testing.T, p corebpf.DevicePolicyValue) {
	t.Helper()
	//: a policy Validate accepts.
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	prog := mustVerify(t, deviceInstructions(p))
	//: every request.
	for _, r := range deviceRequests() {
		got := prog.run(t, r.context(), nil, nil) == uint64(retAllow)
		//: the documented decision.
		if want := wantDevice(p, r); got != want {
			t.Fatalf("policy %+v, request %+v: got allow=%v, want %v", p, r, got, want)
		}
	}
}

// TestDevicePoliciesDecideAsDocumented compiles hand-written policies — a
// container's allow-list, a deny-list, rules every field of which is a
// wildcard, numbers past int32 — and checks each decision.
func TestDevicePoliciesDecideAsDocumented(t *testing.T) {
	any := corebpf.AnyNumber
	cases := map[string]corebpf.DevicePolicyValue{
		"empty-deny":  {Default: corebpf.Deny},
		"empty-allow": {Default: corebpf.Allow},
		"container-allow-list": {Default: corebpf.Deny, Rules: []corebpf.DeviceRuleValue{
			{Type: corebpf.DeviceChar, Major: 1, Minor: 3, Access: corebpf.AccessAll, Verdict: corebpf.Allow},
			{Type: corebpf.DeviceChar, Major: 1, Minor: 5, Access: corebpf.AccessAll, Verdict: corebpf.Allow},
			{Type: corebpf.DeviceChar, Major: 136, Minor: any, Access: corebpf.AccessRead | corebpf.AccessWrite, Verdict: corebpf.Allow},
			{Type: corebpf.DeviceAny, Major: any, Minor: any, Access: corebpf.AccessMknod, Verdict: corebpf.Allow},
		}},
		"deny-write-of-one": {Default: corebpf.Allow, Rules: []corebpf.DeviceRuleValue{
			{Type: corebpf.DeviceBlock, Major: 5, Minor: 0, Access: corebpf.AccessWrite, Verdict: corebpf.Deny},
		}},
		"shadowing-rule-ends-the-program": {Default: corebpf.Allow, Rules: []corebpf.DeviceRuleValue{
			{Type: corebpf.DeviceChar, Major: 1, Minor: 3, Access: corebpf.AccessRead, Verdict: corebpf.Allow},
			{Type: corebpf.DeviceAny, Major: any, Minor: any, Access: corebpf.AccessAll, Verdict: corebpf.Deny},
			{Type: corebpf.DeviceChar, Major: 1, Minor: 5, Access: corebpf.AccessAll, Verdict: corebpf.Allow},
		}},
		"numbers-past-int32": {Default: corebpf.Deny, Rules: []corebpf.DeviceRuleValue{
			{Type: corebpf.DeviceChar, Major: math.MaxUint32, Minor: math.MaxInt32 + 1, Access: corebpf.AccessAll, Verdict: corebpf.Allow},
			{Type: corebpf.DeviceBlock, Major: math.MaxInt32 + 1, Minor: math.MaxUint32, Access: corebpf.AccessRead, Verdict: corebpf.Allow},
		}},
	}
	//: each policy against every request.
	for name, p := range cases {
		t.Run(name, func(t *testing.T) { checkDevicePolicy(t, p) })
	}
}

// TestRandomDevicePoliciesDecideAsDocumented compiles policies drawn from
// a fixed seed, so a failure replays.
func TestRandomDevicePoliciesDecideAsDocumented(t *testing.T) {
	rng := rand.New(rand.NewPCG(731, 6))
	numbers := []int64{corebpf.AnyNumber, 0, 1, 3, 5, 136, math.MaxInt32 + 1, math.MaxUint32}
	//: two hundred policies of up to eight rules.
	for range 200 {
		p := corebpf.DevicePolicyValue{Default: corebpf.Verdict(1 + rng.IntN(2))}
		//: each rule drawn field by field.
		for range rng.IntN(9) {
			p.Rules = append(p.Rules, corebpf.DeviceRuleValue{
				Type:    corebpf.DeviceType(1 + rng.IntN(3)),
				Major:   numbers[rng.IntN(len(numbers))],
				Minor:   numbers[rng.IntN(len(numbers))],
				Access:  corebpf.DeviceAccess(1 + rng.IntN(7)),
				Verdict: corebpf.Verdict(1 + rng.IntN(2)),
			})
		}
		checkDevicePolicy(t, p)
	}
}
