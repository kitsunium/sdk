package cgroup

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strings"
	"testing"

	"github.com/cilium/ebpf/asm"
)

// The tests run the instructions this package compiles on a small
// interpreter of the subset they use, so what a program decides is checked
// on every platform and without privilege; the kernel's verifier and its
// helpers are the live suite's (live_linux_external_test.go). The
// interpreter is strict where the verifier is: a jump to a missing label, a
// read of the stack nothing wrote, a helper argument outside the stack, a
// register read after a call clobbered it, a return value other than 0 or 1.

// Address regions of the interpreter: the stack, the context, map values
// and map handles each live in their own range.
const (
	vmStackBase  = 0x1_0000_0000
	vmStackSize  = 512
	vmCtxBase    = 0x2_0000_0000
	vmValueBase  = 0x3_0000_0000
	vmValueSpan  = 0x1_0000
	vmMapBase    = 0x4_0000_0000
	vmClobbered  = 0xDEAD_BEEF_DEAD_BEEF
	vmMaxSteps   = 100_000
	vmMaxInsns   = 4096
	vmStackFrame = vmStackBase + vmStackSize
)

// efault is -EFAULT as a register holds it: what skb_load_bytes returns
// for a read past the packet.
var efault = func() uint64 { e := int64(-14); return uint64(e) }()

// vmMap is a map as the interpreter serves it: an LPM trie or an array.
type vmMap struct {
	keySize int
	lpm     map[netip.Prefix][]byte
	array   [][]byte
}

// lookup returns the value a key selects, nil on a miss.
func (m *vmMap) lookup(key []byte) []byte {
	//: an array is indexed by a host u32.
	if m.array != nil {
		i := binary.NativeEndian.Uint32(key)
		//: past the end.
		if int(i) >= len(m.array) {
			return nil
		}
		//: the slot.
		return m.array[i]
	}
	bits := int(binary.NativeEndian.Uint32(key[:4]))
	addr, ok := netip.AddrFromSlice(key[4:])
	//: a key the program built wrong.
	if !ok {
		return nil
	}
	var best []byte
	bestBits := -1
	//: the longest prefix holding the address, within the key's length.
	for p, v := range m.lpm {
		//: a prefix of the other family, longer than the key, or not holding it.
		if p.Addr().BitLen() != addr.BitLen() || p.Bits() > bits || !p.Contains(addr) {
			continue
		}
		//: a longer match.
		if p.Bits() > bestBits {
			best, bestBits = v, p.Bits()
		}
	}
	return best
}

// vm is one run of a program.
type vm struct {
	t       *testing.T
	regs    [11]uint64
	stack   [vmStackSize]byte
	written [vmStackSize]bool
	ctx     []byte
	packet  []byte
	maps    map[string]*vmMap
	handles []*vmMap
	values  [][]byte
}

// program is a verified program, its labels resolved.
type program struct {
	insns  asm.Instructions
	labels map[string]int
}

// mustVerify verifies insns once, for many runs.
func mustVerify(t *testing.T, insns asm.Instructions) program {
	t.Helper()
	labels, err := verifyProgram(insns)
	//: a program the verifier would refuse never runs.
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return program{insns: insns, labels: labels}
}

// run runs the program over a context, a packet and maps, and returns R0.
func (p program) run(t *testing.T, ctx, packet []byte, maps map[string]*vmMap) uint64 {
	t.Helper()
	m := &vm{t: t, ctx: ctx, packet: packet, maps: maps}
	m.regs[asm.R1] = vmCtxBase
	m.regs[asm.R10] = vmStackFrame
	//: one instruction per step, bounded.
	for pc, steps := 0, 0; steps < vmMaxSteps; steps++ {
		//: off the end: verifyProgram refuses it, this guards the loop.
		if pc >= len(p.insns) {
			t.Fatalf("pc %d past the end", pc)
		}
		next, exit := m.step(p.insns[pc], p.labels, pc)
		//: exit returns R0.
		if exit {
			r0 := m.regs[asm.R0]
			//: the verifier holds a cgroup program's return to 0 or 1.
			if r0 > 1 {
				t.Fatalf("returns %d, not 0 or 1", r0)
			}
			return r0
		}
		pc = next
	}
	t.Fatalf("no exit within %d steps", vmMaxSteps)
	return 0
}

// verifyProgram is what the verifier's CFG check refuses: a jump to a label
// that does not exist, an instruction nothing reaches, a fall off the end,
// an exit whose R0 is not a constant 0 or 1 set right before it, more
// instructions than an unprivileged program may hold. It also marshals the
// program, which resolves the jumps. It returns the labels.
func verifyProgram(insns asm.Instructions) (map[string]int, error) {
	//: a bounded program.
	if len(insns) == 0 || len(insns) > vmMaxInsns {
		return nil, fmt.Errorf("%d instructions", len(insns))
	}
	labels := make(map[string]int)
	//: each label once.
	for i, ins := range insns {
		//: unlabelled.
		if ins.Symbol() == "" {
			continue
		}
		//: a label twice.
		if _, dup := labels[ins.Symbol()]; dup {
			return nil, fmt.Errorf("label %q twice", ins.Symbol())
		}
		labels[ins.Symbol()] = i
	}
	reached := make([]bool, len(insns))
	todo := []int{0}
	//: every path from the first instruction.
	for len(todo) > 0 {
		pc := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		//: off the end.
		if pc >= len(insns) {
			return nil, fmt.Errorf("falls off the end after %d", pc-1)
		}
		//: visited.
		if reached[pc] {
			continue
		}
		reached[pc] = true
		ins := insns[pc]
		op := ins.OpCode.JumpOp()
		isJump := ins.OpCode.Class().IsJump()
		//: exit ends the path; its R0 must be a constant 0 or 1.
		if isJump && op == asm.Exit {
			//: mov r0, 0 or 1, right before.
			if pc == 0 || !constantReturn(insns[pc-1]) {
				return nil, fmt.Errorf("exit at %d without a constant 0 or 1 in R0", pc)
			}
			continue
		}
		//: a jump: its target, and the next one unless it always jumps.
		if isJump && op != asm.Call {
			target, ok := labels[ins.Reference()]
			//: a missing label.
			if !ok {
				return nil, fmt.Errorf("instruction %d jumps to missing %q", pc, ins.Reference())
			}
			todo = append(todo, target)
			//: Ja never falls through.
			if op == asm.Ja {
				continue
			}
		}
		todo = append(todo, pc+1)
	}
	//: no dead code.
	for i, ok := range reached {
		//: unreachable.
		if !ok {
			return nil, fmt.Errorf("instruction %d (%v) is unreachable", i, insns[i])
		}
	}
	//: the jumps resolve; the encoding itself is Linux's (asm refuses a
	//: Linux-only helper elsewhere), the decisions stay the interpreter's.
	if err := marshalable(insns).Marshal(io.Discard, hostOrder()); err != nil && (runtime.GOOS == "linux" || !strings.Contains(err.Error(), "not supported on "+runtime.GOOS)) {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return labels, nil
}

// hostOrder is this host's byte order as asm.Marshal names it: it knows
// binary.LittleEndian and binary.BigEndian, not binary.NativeEndian.
func hostOrder() binary.ByteOrder {
	//: a big-endian host reads {0, 1} as 1.
	if binary.NativeEndian.Uint16([]byte{0, 1}) == 1 {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// constantReturn reports whether ins is mov r0, 0 or mov r0, 1.
func constantReturn(ins asm.Instruction) bool {
	return ins.OpCode == asm.Mov.Op(asm.ImmSource) && ins.Dst == asm.R0 && (ins.Constant == 0 || ins.Constant == 1)
}

// fakeFD stands for a map in a marshalled program.
type fakeFD int

// FD is the descriptor.
func (f fakeFD) FD() int { return int(f) }

// marshalable copies insns with their map references associated to a fake
// descriptor, so Marshal resolves the jumps.
func marshalable(insns asm.Instructions) asm.Instructions {
	out := make(asm.Instructions, len(insns))
	copy(out, insns)
	//: each map load gets a descriptor.
	for i := range out {
		//: a map reference.
		if out[i].IsLoadFromMap() && out[i].Reference() != "" {
			_ = out[i].AssociateMap(fakeFD(3 + i))
		}
	}
	return out
}

// step runs one instruction and returns the next pc, or exit.
func (m *vm) step(ins asm.Instruction, labels map[string]int, pc int) (int, bool) {
	class := ins.OpCode.Class()
	//: by class.
	switch {
	case class.IsALU():
		m.alu(ins)
	case class == asm.LdClass:
		m.loadImm(ins)
	case class == asm.LdXClass:
		m.regs[ins.Dst] = m.load(m.read(ins.Src)+uint64(int64(ins.Offset)), ins.OpCode.Size())
	case class == asm.StClass:
		m.store(m.read(ins.Dst)+uint64(int64(ins.Offset)), ins.OpCode.Size(), uint64(ins.Constant))
	case class == asm.StXClass:
		m.storeReg(ins)
	case class.IsJump():
		return m.jump(ins, labels, pc)
	default:
		m.t.Fatalf("unknown instruction %v", ins)
	}
	return pc + 1, false
}

// read returns a register, refusing one a call clobbered.
func (m *vm) read(r asm.Register) uint64 {
	v := m.regs[r]
	//: R1 to R5 are not preserved across a call.
	if v == vmClobbered {
		m.t.Fatalf("R%d read after a call clobbered it", r)
	}
	return v
}

// operand is an instruction's source: its immediate or its register.
func (m *vm) operand(ins asm.Instruction) uint64 {
	//: an immediate is sign-extended.
	if ins.OpCode.Source() == asm.ImmSource {
		return uint64(ins.Constant)
	}
	return m.read(ins.Src)
}

// alu runs an arithmetic instruction.
func (m *vm) alu(ins asm.Instruction) {
	op := ins.OpCode.ALUOp()
	//: the byte swaps: to big endian swaps on a little-endian host.
	if op == asm.Swap {
		m.regs[ins.Dst] = swap(m.read(ins.Dst), ins.OpCode.Endianness(), int(ins.Constant))
		return
	}
	src := m.operand(ins)
	dst := uint64(0)
	//: mov does not read its destination.
	if op != asm.Mov {
		dst = m.read(ins.Dst)
	}
	var v uint64
	//: the operations the programs use.
	switch op {
	case asm.Mov:
		v = src
	case asm.Add:
		v = dst + src
	case asm.And:
		v = dst & src
	case asm.Or:
		v = dst | src
	case asm.RSh:
		v = dst >> (src & 63)
	case asm.LSh:
		v = dst << (src & 63)
	default:
		m.t.Fatalf("unsupported ALU op %v", op)
	}
	//: 32-bit ALU zero-extends.
	if ins.OpCode.Class() == asm.ALUClass {
		v = uint64(uint32(v))
	}
	m.regs[ins.Dst] = v
}

// swap is BPF_END: to the endianness asked, from this host's.
func swap(v uint64, e asm.Endianness, bits int) uint64 {
	native := binary.NativeEndian.Uint16([]byte{1, 0}) == 1
	mustSwap := (e == asm.BE) == native
	//: the width.
	switch bits {
	case 16:
		x := uint16(v)
		//: reversed.
		if mustSwap {
			x = x<<8 | x>>8
		}
		return uint64(x)
	case 32:
		x := uint32(v)
		//: reversed.
		if mustSwap {
			b := binary.LittleEndian.AppendUint32(nil, x)
			x = binary.BigEndian.Uint32(b)
		}
		return uint64(x)
	default:
		//: reversed.
		if mustSwap {
			b := binary.LittleEndian.AppendUint64(nil, v)
			return binary.BigEndian.Uint64(b)
		}
		return v
	}
}

// loadImm is a 64-bit immediate, or a map's handle.
func (m *vm) loadImm(ins asm.Instruction) {
	//: a map reference becomes the handle of the map of that name.
	if ins.IsLoadFromMap() {
		mp, ok := m.maps[ins.Reference()]
		//: a map the test did not give.
		if !ok {
			m.t.Fatalf("map %q not provided", ins.Reference())
		}
		m.handles = append(m.handles, mp)
		m.regs[ins.Dst] = vmMapBase + uint64(len(m.handles)-1)
		return
	}
	m.regs[ins.Dst] = uint64(ins.Constant)
}

// storeReg is a register store or an atomic add.
func (m *vm) storeReg(ins asm.Instruction) {
	addr := m.read(ins.Dst) + uint64(int64(ins.Offset))
	//: BPF_ATOMIC with BPF_ADD, the only atomic the programs use.
	if ins.OpCode.Mode() == asm.AtomicMode {
		//: add without fetch.
		if ins.OpCode.AtomicOp() != asm.AddAtomic {
			m.t.Fatalf("unsupported atomic %v", ins)
		}
		m.store(addr, ins.OpCode.Size(), m.load(addr, ins.OpCode.Size())+m.read(ins.Src))
		return
	}
	m.store(addr, ins.OpCode.Size(), m.read(ins.Src))
}

// mem returns the bytes at addr, refusing an address outside every region.
func (m *vm) mem(addr uint64, n int, write bool) []byte {
	//: by region.
	switch {
	case addr >= vmStackBase && addr+uint64(n) <= vmStackFrame:
		off := int(addr - vmStackBase)
		//: the verifier refuses a read of a stack byte nothing wrote.
		for i := off; i < off+n; i++ {
			//: written, or being written.
			if !write && !m.written[i] {
				m.t.Fatalf("read of uninitialised stack at %d", i-vmStackSize)
			}
			m.written[i] = m.written[i] || write
		}
		return m.stack[off : off+n]
	case addr >= vmCtxBase && addr+uint64(n) <= vmCtxBase+uint64(len(m.ctx)):
		//: the context is read only.
		if write {
			m.t.Fatalf("write to the context at %d", addr-vmCtxBase)
		}
		off := int(addr - vmCtxBase)
		return m.ctx[off : off+n]
	case addr >= vmValueBase:
		i := int((addr - vmValueBase) / vmValueSpan)
		off := int((addr - vmValueBase) % vmValueSpan)
		//: inside one value.
		if i < len(m.values) && off+n <= len(m.values[i]) {
			return m.values[i][off : off+n]
		}
	}
	m.t.Fatalf("access of %d bytes at %#x outside every region", n, addr)
	return nil
}

// load reads size bytes as a host integer.
func (m *vm) load(addr uint64, size asm.Size) uint64 {
	b := m.mem(addr, size.Sizeof(), false)
	//: by width.
	switch size {
	case asm.Byte:
		return uint64(b[0])
	case asm.Half:
		return uint64(binary.NativeEndian.Uint16(b))
	case asm.Word:
		return uint64(binary.NativeEndian.Uint32(b))
	default:
		return binary.NativeEndian.Uint64(b)
	}
}

// store writes size bytes of v as a host integer.
func (m *vm) store(addr uint64, size asm.Size, v uint64) {
	b := m.mem(addr, size.Sizeof(), true)
	//: by width.
	switch size {
	case asm.Byte:
		b[0] = byte(v)
	case asm.Half:
		binary.NativeEndian.PutUint16(b, uint16(v))
	case asm.Word:
		binary.NativeEndian.PutUint32(b, uint32(v))
	default:
		binary.NativeEndian.PutUint64(b, v)
	}
}

// jump runs a jump, a call or the exit.
func (m *vm) jump(ins asm.Instruction, labels map[string]int, pc int) (int, bool) {
	op := ins.OpCode.JumpOp()
	//: the three that do not compare.
	switch op {
	case asm.Exit:
		return 0, true
	case asm.Call:
		m.call(asm.BuiltinFunc(ins.Constant))
		return pc + 1, false
	case asm.Ja:
		return labels[ins.Reference()], false
	}
	dst := m.read(ins.Dst)
	src := m.operand(ins)
	//: 32-bit jumps compare the low halves.
	if ins.OpCode.Class() == asm.Jump32Class {
		dst, src = uint64(uint32(dst)), uint64(uint32(src))
	}
	var taken bool
	//: the comparisons the programs use.
	switch op {
	case asm.JEq:
		taken = dst == src
	case asm.JNE:
		taken = dst != src
	case asm.JGT:
		taken = dst > src
	case asm.JGE:
		taken = dst >= src
	case asm.JLT:
		taken = dst < src
	case asm.JLE:
		taken = dst <= src
	default:
		m.t.Fatalf("unsupported jump %v", op)
	}
	//: the label, or the next instruction.
	if taken {
		return labels[ins.Reference()], false
	}
	return pc + 1, false
}

// call runs a helper, then clobbers R1 to R5.
func (m *vm) call(fn asm.BuiltinFunc) {
	//: the helpers the programs call.
	switch fn {
	case asm.FnSkbLoadBytes:
		m.skbLoadBytes()
	case asm.FnMapLookupElem:
		m.mapLookup()
	default:
		m.t.Fatalf("unsupported helper %v", fn)
	}
	//: caller-saved registers are garbage after a call.
	for r := asm.R1; r <= asm.R5; r++ {
		m.regs[r] = vmClobbered
	}
}

// skbLoadBytes is bpf_skb_load_bytes(ctx, offset, to, len).
func (m *vm) skbLoadBytes() {
	//: the context.
	if m.read(asm.R1) != vmCtxBase {
		m.t.Fatalf("skb_load_bytes: R1 is not the context")
	}
	off, to, n := m.read(asm.R2), m.read(asm.R3), int(m.read(asm.R4))
	//: the destination is the stack.
	if to < vmStackBase || to+uint64(n) > vmStackFrame {
		m.t.Fatalf("skb_load_bytes: destination outside the stack")
	}
	//: a short packet.
	if off+uint64(n) > uint64(len(m.packet)) {
		m.regs[asm.R0] = efault
		return
	}
	copy(m.mem(to, n, true), m.packet[off:off+uint64(n)])
	m.regs[asm.R0] = 0
}

// mapLookup is bpf_map_lookup_elem(map, key).
func (m *vm) mapLookup() {
	h := m.read(asm.R1)
	//: a map handle.
	if h < vmMapBase || h-vmMapBase >= uint64(len(m.handles)) {
		m.t.Fatalf("map_lookup_elem: R1 is not a map")
	}
	mp := m.handles[h-vmMapBase]
	key := m.read(asm.R2)
	//: the key is on the stack.
	if key < vmStackBase || key+uint64(mp.keySize) > vmStackFrame {
		m.t.Fatalf("map_lookup_elem: key outside the stack")
	}
	v := mp.lookup(m.mem(key, mp.keySize, false))
	//: a miss is NULL.
	if v == nil {
		m.regs[asm.R0] = 0
		return
	}
	m.values = append(m.values, v)
	m.regs[asm.R0] = vmValueBase + uint64(len(m.values)-1)*vmValueSpan
}

// TestVerifyProgramRefusesWhatTheVerifierRefuses keeps the interpreter's
// check honest: an unreachable instruction, a missing label, an exit
// without a constant and a fall off the end are each refused.
func TestVerifyProgramRefusesWhatTheVerifierRefuses(t *testing.T) {
	cases := map[string]asm.Instructions{
		"unreachable": {asm.Mov.Imm(asm.R0, 1), asm.Return(), asm.Mov.Imm(asm.R0, 0), asm.Return()},
		"missing":     {asm.Ja.Label("nowhere"), asm.Mov.Imm(asm.R0, 1), asm.Return()},
		"unknown-r0":  {asm.Mov.Imm(asm.R0, 2), asm.Return()},
		"fall-off":    {asm.Mov.Imm(asm.R0, 1)},
	}
	//: each must fail the check.
	for name, insns := range cases {
		//: refused.
		if _, err := verifyProgram(insns); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
