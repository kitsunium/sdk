package cgroup

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// bindRequest is one bind a cgroup_sock_addr program is asked about.
type bindRequest struct {
	port     uint16
	protocol uint32
}

// context is the request as struct bpf_sock_addr: user_port in network
// byte order in its low half, protocol a host u32.
func (r bindRequest) context() []byte {
	b := make([]byte, 40)
	binary.BigEndian.PutUint16(b[24:], r.port)
	binary.NativeEndian.PutUint32(b[36:], r.protocol)
	return b
}

// wantBind is the policy's decision for one family, read from its
// documentation: the first rule of the family, the transport and the range
// decides.
func wantBind(p corebpf.BindPolicyValue, family corebpf.Family, r bindRequest) bool {
	protocols := map[corebpf.Transport]uint32{corebpf.TransportTCP: 6, corebpf.TransportUDP: 17}
	//: the rules in order.
	for _, rule := range p.Rules {
		familyOK := rule.Family == corebpf.FamilyAny || rule.Family == family
		transportOK := rule.Transport == corebpf.TransportAny || protocols[rule.Transport] == r.protocol
		portOK := r.port >= rule.Ports.First && r.port <= rule.Ports.Last
		//: the first match decides.
		if familyOK && transportOK && portOK {
			return rule.Verdict == corebpf.Allow
		}
	}
	return p.Default == corebpf.Allow
}

// checkBindPolicy runs every request through both programs of the policy.
func checkBindPolicy(t *testing.T, p corebpf.BindPolicyValue) {
	t.Helper()
	//: a policy Validate accepts.
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	//: each hook.
	for _, family := range []corebpf.Family{corebpf.FamilyIPv4, corebpf.FamilyIPv6} {
		prog := mustVerify(t, bindInstructions(family, p))
		//: each port and protocol.
		for _, port := range []uint16{0, 1, 22, 80, 1023, 1024, 8080, 40000, 40001, math.MaxUint16 - 1, math.MaxUint16} {
			for _, protocol := range []uint32{6, 17, 132} {
				r := bindRequest{port: port, protocol: protocol}
				got := prog.run(t, r.context(), nil, nil) == uint64(retAllow)
				//: the documented decision.
				if want := wantBind(p, family, r); got != want {
					t.Fatalf("policy %+v, family %d, request %+v: got allow=%v, want %v", p, family, r, got, want)
				}
			}
		}
	}
}

// TestBindPoliciesDecideAsDocumented compiles hand-written policies: the
// privileged ports denied, one port allowed on one family, a rule that
// shadows the rest, a range at both edges of the port space.
func TestBindPoliciesDecideAsDocumented(t *testing.T) {
	cases := map[string]corebpf.BindPolicyValue{
		"empty-deny":  {Default: corebpf.Deny},
		"empty-allow": {Default: corebpf.Allow},
		"privileged-ports-denied": {Default: corebpf.Allow, Rules: []corebpf.BindRuleValue{
			{Family: corebpf.FamilyAny, Transport: corebpf.TransportAny, Ports: corebpf.PortRangeValue{First: 1, Last: 1023}, Verdict: corebpf.Deny},
		}},
		"one-port-on-ipv6-tcp": {Default: corebpf.Deny, Rules: []corebpf.BindRuleValue{
			{Family: corebpf.FamilyIPv6, Transport: corebpf.TransportTCP, Ports: corebpf.PortRangeValue{First: 8080, Last: 8080}, Verdict: corebpf.Allow},
			{Family: corebpf.FamilyAny, Transport: corebpf.TransportUDP, Ports: corebpf.PortRangeValue{First: 0, Last: 0}, Verdict: corebpf.Allow},
		}},
		"shadowing-rule-ends-the-program": {Default: corebpf.Allow, Rules: []corebpf.BindRuleValue{
			{Family: corebpf.FamilyIPv4, Transport: corebpf.TransportTCP, Ports: corebpf.PortRangeValue{First: 22, Last: 22}, Verdict: corebpf.Allow},
			{Family: corebpf.FamilyIPv4, Transport: corebpf.TransportAny, Ports: corebpf.PortRangeValue{First: 0, Last: math.MaxUint16}, Verdict: corebpf.Deny},
			{Family: corebpf.FamilyAny, Transport: corebpf.TransportTCP, Ports: corebpf.PortRangeValue{First: 80, Last: 80}, Verdict: corebpf.Allow},
		}},
		"edges": {Default: corebpf.Allow, Rules: []corebpf.BindRuleValue{
			{Family: corebpf.FamilyAny, Transport: corebpf.TransportUDP, Ports: corebpf.PortRangeValue{First: 40000, Last: math.MaxUint16}, Verdict: corebpf.Deny},
			{Family: corebpf.FamilyAny, Transport: corebpf.TransportTCP, Ports: corebpf.PortRangeValue{First: 0, Last: 1}, Verdict: corebpf.Deny},
		}},
	}
	//: each policy against every request.
	for name, p := range cases {
		t.Run(name, func(t *testing.T) { checkBindPolicy(t, p) })
	}
}

// TestRandomBindPoliciesDecideAsDocumented compiles policies drawn from a
// fixed seed, so a failure replays.
func TestRandomBindPoliciesDecideAsDocumented(t *testing.T) {
	rng := rand.New(rand.NewPCG(731, 8))
	edges := []uint16{0, 1, 22, 80, 1023, 1024, 8080, 40000, math.MaxUint16}
	//: two hundred policies of up to eight rules.
	for range 200 {
		p := corebpf.BindPolicyValue{Default: corebpf.Verdict(1 + rng.IntN(2))}
		//: each rule drawn field by field, its range ordered.
		for range rng.IntN(9) {
			a, b := edges[rng.IntN(len(edges))], edges[rng.IntN(len(edges))]
			p.Rules = append(p.Rules, corebpf.BindRuleValue{
				Family:    corebpf.Family(1 + rng.IntN(3)),
				Transport: corebpf.Transport(1 + rng.IntN(3)),
				Ports:     corebpf.PortRangeValue{First: min(a, b), Last: max(a, b)},
				Verdict:   corebpf.Verdict(1 + rng.IntN(2)),
			})
		}
		checkBindPolicy(t, p)
	}
}
