package cgroup

import (
	"encoding/binary"
	"net/netip"

	"github.com/cilium/ebpf/asm"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// The context a cgroup_skb program reads, struct __sk_buff of
// include/uapi/linux/bpf.h, and the packet it sees, which starts at the
// network header on both hooks.
const (
	skbLen       int16  = 0
	skbProtocol  int16  = 16
	etherTypeIP  uint16 = 0x0800 // ETH_P_IP
	etherTypeIP6 uint16 = 0x86DD // ETH_P_IPV6
	ip4Source    int32  = 12
	ip4Dest      int32  = 16
	ip6Source    int32  = 8
	ip6Dest      int32  = 24
	ip4Len       int32  = 4
	ip6Len       int32  = 16
	ip4Bits      int64  = 32
	ip6Bits      int64  = 128
)

// The maps a cgroup_skb pair shares, by the name its instructions reference
// them with: an LPM trie per family, keyed {prefixlen u32, address}, whose
// value says whether the prefix is allowed; and the counters, an array of
// two — ingress, egress — of four u64: packets, bytes, dropped packets,
// dropped bytes.
const (
	mapLPM4               = "lpm4"
	mapLPM6               = "lpm6"
	mapCounters           = "counters"
	u32Size        uint32 = 4
	lpmValueSize   uint32 = 4
	lpmAllow       int32  = 1
	lpmDeny        int32  = 0
	counterSlots   uint32 = 2
	counterSize    uint32 = 8
	counterFields  uint32 = 4
	countersValue  uint32 = counterSize * counterFields
	counterPackets int16  = 0
	counterBytes   int16  = 8
	counterDropped int16  = 16
	counterDropB   int16  = 24
)

// The stack the program keeps its keys in, below the frame pointer: the
// IPv4 key at -8 (its address at -4), the IPv6 key at -20 (its address at
// -16), the counters' index at -24.
const (
	stackKey4     int16 = -8
	stackAddr4    int16 = -4
	stackKey6     int16 = -20
	stackAddr6    int16 = -16
	stackCountKey int16 = -24
)

// direction is one hook of a cgroup_skb pair: the counters' slot it
// increments and where the peer's address is in the packet — the source of
// a received one, the destination of a sent one.
type direction struct {
	slot  int64
	addr4 int32
	addr6 int32
}

// family is one address family's lookup: the label its code starts at, the
// trie it reads, the address's size and bit length, and where its key and
// the address in it sit on the stack.
type family struct {
	label string
	trie  string
	size  int32
	bits  int64
	key   int16
	addr  int16
}

var (
	// ingress is BPF_CGROUP_INET_INGRESS.
	ingress = direction{slot: 0, addr4: ip4Source, addr6: ip6Source}
	// egress is BPF_CGROUP_INET_EGRESS.
	egress = direction{slot: 1, addr4: ip4Dest, addr6: ip6Dest}
	// ipv4 is the IPv4 lookup.
	ipv4 = family{label: "ipv4", trie: mapLPM4, size: ip4Len, bits: ip4Bits, key: stackKey4, addr: stackAddr4}
	// ipv6 is the IPv6 lookup.
	ipv6 = family{label: "ipv6", trie: mapLPM6, size: ip6Len, bits: ip6Bits, key: stackKey6, addr: stackAddr6}
)

// skbInstructions compiles one hook of an IP policy: the peer's address is
// read into a key on the stack and looked up in its family's trie; the
// entry's verdict, or def when there is none, lands in R8; the packet and
// its length (R7) are counted, the drop too; R8 is returned.
func skbInstructions(d direction, def corebpf.Verdict) asm.Instructions {
	insns := asm.Instructions{
		asm.Mov.Reg(asm.R6, asm.R1),
		asm.LoadMem(asm.R7, asm.R6, skbLen, asm.Word),
		asm.LoadMem(asm.R2, asm.R6, skbProtocol, asm.Word),
		asm.JEq.Imm(asm.R2, wireEtherType(etherTypeIP), "ipv4"),
		asm.JEq.Imm(asm.R2, wireEtherType(etherTypeIP6), "ipv6"),
		asm.Ja.Label("default"),
	}
	insns = append(insns, lookup(ipv4, d.addr4)...)
	insns = append(insns, asm.Ja.Label("verdict"))
	insns = append(insns, lookup(ipv6, d.addr6)...)
	insns = append(insns,
		// the entry's verdict.
		asm.LoadMem(asm.R1, asm.R0, 0, asm.Word).WithSymbol("verdict"),
		asm.Mov.Imm(asm.R8, retDeny),
		asm.JNE.Imm(asm.R1, lpmAllow, "account"),
		asm.Mov.Imm(asm.R8, retAllow),
		asm.Ja.Label("account"),
		// no entry: the policy's default.
		asm.Mov.Imm(asm.R8, returnValue(def)).WithSymbol("default"),
	)
	return append(insns, account(d.slot)...)
}

// lookup reads the family's address from the packet at offset into its
// key on the stack, prefixed by its bit length, and looks the key up in the
// family's trie; a short packet or a miss jumps to the default, an entry
// leaves its pointer in R0.
func lookup(f family, offset int32) asm.Instructions {
	//: helpers clobber R1 to R5: their arguments are set right before each call.
	return asm.Instructions{
		asm.StoreImm(asm.R10, f.key, f.bits, asm.Word).WithSymbol(f.label),
		asm.Mov.Reg(asm.R1, asm.R6),
		asm.Mov.Imm(asm.R2, offset),
		asm.Mov.Reg(asm.R3, asm.R10),
		asm.Add.Imm(asm.R3, int32(f.addr)),
		asm.Mov.Imm(asm.R4, f.size),
		asm.FnSkbLoadBytes.Call(),
		asm.JNE.Imm(asm.R0, 0, "default"),
		asm.LoadMapPtr(asm.R1, 0).WithReference(f.trie),
		asm.Mov.Reg(asm.R2, asm.R10),
		asm.Add.Imm(asm.R2, int32(f.key)),
		asm.FnMapLookupElem.Call(),
		asm.JEq.Imm(asm.R0, 0, "default"),
	}
}

// account adds the packet (R7 bytes) to the counters' slot, and to its
// drops when R8 is 0, then returns R8 as a constant the verifier can see.
func account(slot int64) asm.Instructions {
	//: one lookup of the slot, then atomic adds into its four u64.
	return asm.Instructions{
		asm.StoreImm(asm.R10, stackCountKey, slot, asm.Word).WithSymbol("account"),
		asm.LoadMapPtr(asm.R1, 0).WithReference(mapCounters),
		asm.Mov.Reg(asm.R2, asm.R10),
		asm.Add.Imm(asm.R2, int32(stackCountKey)),
		asm.FnMapLookupElem.Call(),
		asm.JEq.Imm(asm.R0, 0, "out"),
		asm.Mov.Imm(asm.R1, 1),
		asm.AddAtomic.Mem(asm.R0, asm.R1, asm.DWord, counterPackets),
		asm.AddAtomic.Mem(asm.R0, asm.R7, asm.DWord, counterBytes),
		asm.JNE.Imm(asm.R8, retDeny, "out"),
		asm.Mov.Imm(asm.R1, 1),
		asm.AddAtomic.Mem(asm.R0, asm.R1, asm.DWord, counterDropped),
		asm.AddAtomic.Mem(asm.R0, asm.R7, asm.DWord, counterDropB),
		asm.JEq.Imm(asm.R8, retDeny, "drop").WithSymbol("out"),
		asm.Mov.Imm(asm.R0, retAllow),
		asm.Return(),
		asm.Mov.Imm(asm.R0, retDeny).WithSymbol("drop"),
		asm.Return(),
	}
}

// wireEtherType is an EtherType as __sk_buff.protocol holds it: in network
// byte order, read as a host integer.
func wireEtherType(t uint16) int32 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], t)
	//: the two bytes as this host reads them.
	return int32(binary.NativeEndian.Uint16(b[:]))
}

// prefixKey is the LPM trie key of a prefix: its length as a host u32, then
// its masked address.
func prefixKey(p netip.Prefix) []byte {
	key := binary.NativeEndian.AppendUint32(nil, uint32(p.Bits()))
	//: the address, its bits past the length cleared.
	return append(key, p.Masked().Addr().AsSlice()...)
}

// prefixValue is the LPM trie value of a verdict.
func prefixValue(v corebpf.Verdict) []byte {
	allow := lpmDeny
	//: only Allow is 1.
	if v == corebpf.Allow {
		allow = lpmAllow
	}
	//: a host u32.
	return binary.NativeEndian.AppendUint32(nil, uint32(allow))
}
