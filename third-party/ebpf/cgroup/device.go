package cgroup

import (
	"math"
	"strconv"

	"github.com/cilium/ebpf/asm"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// The context a cgroup_device program reads, struct bpf_cgroup_dev_ctx of
// include/uapi/linux/bpf.h: access_type is (BPF_DEVCG_ACC_* << 16) |
// BPF_DEVCG_DEV_*, then the major and the minor number.
const (
	devCtxAccessType int16 = 0
	devCtxMajor      int16 = 4
	devCtxMinor      int16 = 8
	devTypeBlock     int32 = 1 // BPF_DEVCG_DEV_BLOCK
	devTypeChar      int32 = 2 // BPF_DEVCG_DEV_CHAR
	devAccessShift   int32 = 16
	devTypeMask      int32 = 0xFFFF
	devAccessMask    int32 = 0xFFFF
	accessMknod      int32 = 1 // BPF_DEVCG_ACC_MKNOD
	accessRead       int32 = 2 // BPF_DEVCG_ACC_READ
	accessWrite      int32 = 4 // BPF_DEVCG_ACC_WRITE
	accessAll        int32 = accessMknod | accessRead | accessWrite
)

// The values a cgroup program returns: 1 lets the operation through, 0
// refuses it.
const (
	retAllow int32 = 1
	retDeny  int32 = 0
)

// deviceInstructions compiles a device policy into one cgroup_device
// program, the rules in a straight line: the request's type, accesses,
// major and minor number in R2 to R5, each rule a few comparisons that jump
// to the next rule on a mismatch, then the rule's verdict; the policy's
// default last.
func deviceInstructions(policy corebpf.DevicePolicyValue) asm.Instructions {
	insns := asm.Instructions{
		asm.LoadMem(asm.R2, asm.R1, devCtxAccessType, asm.Word),
		asm.Mov.Reg(asm.R3, asm.R2),
		asm.RSh.Imm(asm.R3, devAccessShift),
		asm.And.Imm(asm.R3, devAccessMask),
		asm.And.Imm(asm.R2, devTypeMask),
		asm.LoadMem(asm.R4, asm.R1, devCtxMajor, asm.Word),
		asm.LoadMem(asm.R5, asm.R1, devCtxMinor, asm.Word),
	}
	//: one block per rule, each falling through to the next on a mismatch.
	for i, rule := range policy.Rules {
		block := deviceRule(rule, ruleLabel(i+1))
		block[0] = block[0].WithSymbol(ruleLabel(i))
		insns = append(insns, block...)
		//: a rule that compares nothing decides every request: what follows
		//: is unreachable, and the verifier refuses an unreachable instruction.
		if len(block) == len(verdictBlock(rule.Verdict, "")) {
			//: the rule is the program's last word.
			return insns
		}
	}
	//: the default, where the last mismatch lands.
	return append(insns, verdictBlock(policy.Default, ruleLabel(len(policy.Rules)))...)
}

// deviceRule compiles one device rule: a jump to next on each mismatch,
// then the rule's verdict.
func deviceRule(rule corebpf.DeviceRuleValue, next string) asm.Instructions {
	var insns asm.Instructions
	//: a rule for one kind of node compares the request's.
	if rule.Type != corebpf.DeviceAny {
		insns = append(insns, asm.JNE.Imm(asm.R2, kernelDeviceType(rule.Type), next))
	}
	insns = append(insns, accessCheck(rule, next)...)
	insns = append(insns, numberCheck(asm.R4, rule.Major, next)...)
	insns = append(insns, numberCheck(asm.R5, rule.Minor, next)...)
	//: every comparison held: the rule decides.
	return append(insns, asm.Mov.Imm(asm.R0, returnValue(rule.Verdict)), asm.Return())
}

// accessCheck compiles the access half of a device rule. An Allow rule
// matches a request whose every access it covers, so a bit outside its set
// is a mismatch; a Deny rule matches a request one of whose accesses it
// covers, so an empty intersection is a mismatch.
func accessCheck(rule corebpf.DeviceRuleValue, next string) asm.Instructions {
	set := kernelAccess(rule.Access)
	//: a rule covering every access matches every request either way.
	if set == accessAll {
		//: nothing to compare.
		return nil
	}
	//: allow: the request's bits outside the rule's set must be none.
	if rule.Verdict == corebpf.Allow {
		//: R1 = request &^ set.
		return asm.Instructions{
			asm.Mov.Reg(asm.R1, asm.R3),
			asm.And.Imm(asm.R1, devAccessMask&^set),
			asm.JNE.Imm(asm.R1, 0, next),
		}
	}
	//: deny: the request and the rule's set must share a bit.
	return asm.Instructions{
		asm.Mov.Reg(asm.R1, asm.R3),
		asm.And.Imm(asm.R1, set),
		asm.JEq.Imm(asm.R1, 0, next),
	}
}

// numberCheck compiles the comparison of a major or a minor number held in
// reg, nothing for AnyNumber. A number past int32 is loaded in a register
// first: an immediate is sign-extended and would never equal it.
func numberCheck(reg asm.Register, number int64, next string) asm.Instructions {
	//: the wildcard compares nothing.
	if number == corebpf.AnyNumber {
		//: nothing to compare.
		return nil
	}
	//: a number an immediate holds as it is.
	if number <= math.MaxInt32 {
		//: one comparison.
		return asm.Instructions{asm.JNE.Imm(reg, int32(number), next)}
	}
	//: a larger one through R1.
	return asm.Instructions{
		asm.LoadImm(asm.R1, number, asm.DWord),
		asm.JNE.Reg(reg, asm.R1, next),
	}
}

// kernelDeviceType is a rule's type as the kernel encodes it.
func kernelDeviceType(t corebpf.DeviceType) int32 {
	//: block or character; DeviceAny compares nothing and never comes here.
	if t == corebpf.DeviceBlock {
		//: BPF_DEVCG_DEV_BLOCK.
		return devTypeBlock
	}
	//: BPF_DEVCG_DEV_CHAR.
	return devTypeChar
}

// kernelAccess is an access set as the kernel encodes it.
func kernelAccess(a corebpf.DeviceAccess) int32 {
	var set int32
	//: one bit each, mapped by name rather than by value.
	if a&corebpf.AccessMknod != 0 {
		set |= accessMknod
	}
	//: read.
	if a&corebpf.AccessRead != 0 {
		set |= accessRead
	}
	//: write.
	if a&corebpf.AccessWrite != 0 {
		set |= accessWrite
	}
	//: the kernel's set.
	return set
}

// returnValue is what a cgroup program returns for a verdict.
func returnValue(v corebpf.Verdict) int32 {
	//: only Allow lets the operation through.
	if v == corebpf.Allow {
		//: 1.
		return retAllow
	}
	//: 0.
	return retDeny
}

// verdictBlock returns the verdict, its first instruction labelled.
func verdictBlock(v corebpf.Verdict, label string) asm.Instructions {
	//: a constant in R0: the verifier holds a cgroup program's return to 0 or 1.
	return asm.Instructions{
		asm.Mov.Imm(asm.R0, returnValue(v)).WithSymbol(label),
		asm.Return(),
	}
}

// ruleLabel names the first instruction of rule i; the default is the rule
// past the last.
func ruleLabel(i int) string {
	//: unique within one program.
	return "rule_" + strconv.Itoa(i)
}
