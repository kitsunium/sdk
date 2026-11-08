package cgroup

import (
	"math"

	"github.com/cilium/ebpf/asm"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// The context a cgroup_sock_addr program reads, struct bpf_sock_addr of
// include/uapi/linux/bpf.h: user_port holds the port in network byte order
// in its low 16 bits, protocol the socket's IPPROTO_*.
const (
	sockAddrUserPort int16 = 24
	sockAddrProtocol int16 = 36
	ipprotoTCP       int32 = 6
	ipprotoUDP       int32 = 17
)

// bindInstructions compiles the rules of a bind policy that hold for one
// address family — FamilyIPv4 or FamilyIPv6, the hook the program goes on —
// into one cgroup_sock_addr program: the port in host order in R2, the
// protocol in R3, each rule a few comparisons that jump to the next rule on
// a mismatch, then the rule's verdict; the policy's default last.
func bindInstructions(family corebpf.Family, policy corebpf.BindPolicyValue) asm.Instructions {
	insns := asm.Instructions{
		asm.LoadMem(asm.R2, asm.R1, sockAddrUserPort, asm.Word),
		asm.HostTo(asm.BE, asm.R2, asm.Half),
		asm.LoadMem(asm.R3, asm.R1, sockAddrProtocol, asm.Word),
	}
	n := 0
	//: the rules of this family, in order.
	for _, rule := range policy.Rules {
		//: a rule for the other family is not this hook's.
		if rule.Family != corebpf.FamilyAny && rule.Family != family {
			continue
		}
		block := bindRule(rule, ruleLabel(n+1))
		block[0] = block[0].WithSymbol(ruleLabel(n))
		insns = append(insns, block...)
		n++
		//: a rule that compares nothing decides every bind: what follows is
		//: unreachable, and the verifier refuses an unreachable instruction.
		if len(block) == len(verdictBlock(rule.Verdict, "")) {
			//: the rule is the program's last word.
			return insns
		}
	}
	//: the default, where the last mismatch lands.
	return append(insns, verdictBlock(policy.Default, ruleLabel(n))...)
}

// bindRule compiles one bind rule: a jump to next on each mismatch, then
// the rule's verdict.
func bindRule(rule corebpf.BindRuleValue, next string) asm.Instructions {
	var insns asm.Instructions
	//: a rule for one transport compares the socket's protocol.
	if rule.Transport != corebpf.TransportAny {
		insns = append(insns, asm.JNE.Imm(asm.R3, protocolNumber(rule.Transport), next))
	}
	//: a range that starts above 0 has a floor.
	if rule.Ports.First > 0 {
		insns = append(insns, asm.JLT.Imm(asm.R2, int32(rule.Ports.First), next))
	}
	//: a range that ends below 65535 has a ceiling.
	if rule.Ports.Last < math.MaxUint16 {
		insns = append(insns, asm.JGT.Imm(asm.R2, int32(rule.Ports.Last), next))
	}
	//: every comparison held: the rule decides.
	return append(insns, asm.Mov.Imm(asm.R0, returnValue(rule.Verdict)), asm.Return())
}

// protocolNumber is a transport's IPPROTO_* number.
func protocolNumber(t corebpf.Transport) int32 {
	//: TCP or UDP; TransportAny compares nothing and never comes here.
	if t == corebpf.TransportTCP {
		//: IPPROTO_TCP.
		return ipprotoTCP
	}
	//: IPPROTO_UDP.
	return ipprotoUDP
}
