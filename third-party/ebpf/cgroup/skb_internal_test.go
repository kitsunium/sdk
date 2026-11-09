package cgroup

import (
	"encoding/binary"
	"net/netip"
	"testing"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// skbPacket is one packet a cgroup_skb program sees: its EtherType and its
// bytes from the network header on.
type skbPacket struct {
	etherType uint16
	data      []byte
}

// context is the packet as struct __sk_buff: len, and protocol in network
// byte order.
func (p skbPacket) context() []byte {
	b := make([]byte, 20)
	binary.NativeEndian.PutUint32(b[skbLen:], uint32(len(p.data)))
	binary.BigEndian.PutUint16(b[skbProtocol:], p.etherType)
	return b
}

// ipPacket is a packet of size bytes from src to dst, its header filled
// where the programs read it.
func ipPacket(src, dst netip.Addr, size int) skbPacket {
	//: IPv4: a 20-byte header at least.
	if src.Is4() {
		b := make([]byte, max(size, 20))
		b[0] = 0x45
		s, d := src.As4(), dst.As4()
		copy(b[ip4Source:], s[:])
		copy(b[ip4Dest:], d[:])
		return skbPacket{etherType: etherTypeIP, data: b}
	}
	b := make([]byte, max(size, 40))
	b[0] = 0x60
	s, d := src.As16(), dst.As16()
	copy(b[ip6Source:], s[:])
	copy(b[ip6Dest:], d[:])
	return skbPacket{etherType: etherTypeIP6, data: b}
}

// skbMaps are the maps of a policy as the interpreter serves them.
func skbMaps(p corebpf.SKBPolicyValue) map[string]*vmMap {
	lpm4 := &vmMap{keySize: int(u32Size) + int(ip4Len), lpm: map[netip.Prefix][]byte{}}
	lpm6 := &vmMap{keySize: int(u32Size) + int(ip6Len), lpm: map[netip.Prefix][]byte{}}
	//: each rule in its family's trie, as the backend fills them.
	for _, r := range p.Rules {
		key := prefixKey(r.Prefix)
		bits := int(binary.NativeEndian.Uint32(key))
		addr, _ := netip.AddrFromSlice(key[4:])
		prefix := netip.PrefixFrom(addr, bits)
		//: by family.
		if r.Prefix.Addr().Is4() {
			lpm4.lpm[prefix] = prefixValue(r.Verdict)
		} else {
			lpm6.lpm[prefix] = prefixValue(r.Verdict)
		}
	}
	counters := &vmMap{keySize: int(u32Size), array: [][]byte{make([]byte, countersValue), make([]byte, countersValue)}}
	return map[string]*vmMap{mapLPM4: lpm4, mapLPM6: lpm6, mapCounters: counters}
}

// wantSKB is the policy's decision for a peer, read from its
// documentation: the longest prefix holding the address decides.
func wantSKB(p corebpf.SKBPolicyValue, peer netip.Addr) bool {
	best := -1
	allow := p.Default == corebpf.Allow
	//: the longest prefix holding the peer.
	for _, r := range p.Rules {
		//: a longer one that holds it.
		if r.Prefix.Contains(peer) && r.Prefix.Bits() > best {
			best, allow = r.Prefix.Bits(), r.Verdict == corebpf.Allow
		}
	}
	return allow
}

// counted reads one slot of the counters map.
func counted(maps map[string]*vmMap, slot int) corebpf.TrafficValue {
	b := maps[mapCounters].array[slot]
	return corebpf.TrafficValue{
		Packets:        binary.NativeEndian.Uint64(b[counterPackets:]),
		Bytes:          binary.NativeEndian.Uint64(b[counterBytes:]),
		DroppedPackets: binary.NativeEndian.Uint64(b[counterDropped:]),
		DroppedBytes:   binary.NativeEndian.Uint64(b[counterDropB:]),
	}
}

// TestSKBProgramsDecideAndCountAsDocumented runs packets of both families
// through both hooks: the peer is the source on ingress and the
// destination on egress, the longest prefix decides, a packet no prefix
// holds takes the default, and each program counts every packet, the
// dropped ones apart.
func TestSKBProgramsDecideAndCountAsDocumented(t *testing.T) {
	policy := corebpf.SKBPolicyValue{Default: corebpf.Allow, Rules: []corebpf.PrefixRuleValue{
		{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Verdict: corebpf.Deny},
		{Prefix: netip.MustParsePrefix("10.1.0.0/16"), Verdict: corebpf.Allow},
		{Prefix: netip.MustParsePrefix("10.1.2.3/32"), Verdict: corebpf.Deny},
		{Prefix: netip.MustParsePrefix("fd00::/8"), Verdict: corebpf.Deny},
		{Prefix: netip.MustParsePrefix("fd00:1::/32"), Verdict: corebpf.Allow},
	}}
	//: a policy Validate accepts.
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	local4, local6 := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")
	peers := []string{"10.9.9.9", "10.1.9.9", "10.1.2.3", "203.0.113.7", "fd00::5", "fd00:1::5", "2001:db8::99"}
	//: each hook.
	for _, hook := range []struct {
		name string
		dir  direction
	}{{"ingress", ingress}, {"egress", egress}} {
		maps := skbMaps(policy)
		prog := mustVerify(t, skbInstructions(hook.dir, policy.Default))
		var want corebpf.TrafficValue
		//: each peer, as source on ingress and destination on egress.
		for i, s := range peers {
			peer := netip.MustParseAddr(s)
			local := local4
			//: the local address of the peer's family.
			if peer.Is6() {
				local = local6
			}
			src, dst := peer, local
			//: egress sends to the peer.
			if hook.dir == egress {
				src, dst = local, peer
			}
			pkt := ipPacket(src, dst, 60+i)
			got := prog.run(t, pkt.context(), pkt.data, maps) == uint64(retAllow)
			//: the documented decision.
			if w := wantSKB(policy, peer); got != w {
				t.Fatalf("%s, peer %s: got allow=%v, want %v", hook.name, peer, got, w)
			}
			want.Packets++
			want.Bytes += uint64(len(pkt.data))
			//: a drop is counted apart too.
			if !got {
				want.DroppedPackets++
				want.DroppedBytes += uint64(len(pkt.data))
			}
		}
		//: the slot of the hook, and only it.
		if got := counted(maps, int(hook.dir.slot)); got != want {
			t.Fatalf("%s counted %+v, want %+v", hook.name, got, want)
		}
		//: the other hook's slot is untouched.
		if other := counted(maps, int(1-hook.dir.slot)); other != (corebpf.TrafficValue{}) {
			t.Fatalf("%s counted into the other slot: %+v", hook.name, other)
		}
	}
}

// TestSKBProgramsTakeTheDefaultForWhatTheyCannotRead sends a packet that
// is neither IPv4 nor IPv6, and an IPv4 packet too short to hold its
// addresses: both take the default, and both are counted.
func TestSKBProgramsTakeTheDefaultForWhatTheyCannotRead(t *testing.T) {
	//: both defaults.
	for _, def := range []corebpf.Verdict{corebpf.Allow, corebpf.Deny} {
		policy := corebpf.SKBPolicyValue{Default: def, Rules: []corebpf.PrefixRuleValue{
			{Prefix: netip.MustParsePrefix("0.0.0.0/0"), Verdict: corebpf.Allow},
		}}
		maps := skbMaps(policy)
		prog := mustVerify(t, skbInstructions(egress, def))
		packets := []skbPacket{
			{etherType: 0x0806, data: make([]byte, 28)},
			{etherType: etherTypeIP, data: make([]byte, 10)},
		}
		//: each packet.
		for _, pkt := range packets {
			got := prog.run(t, pkt.context(), pkt.data, maps) == uint64(retAllow)
			//: the default, whatever the rules say.
			if got != (def == corebpf.Allow) {
				t.Fatalf("default %d, ethertype %#x, %d bytes: got allow=%v", def, pkt.etherType, len(pkt.data), got)
			}
		}
		//: both counted.
		if c := counted(maps, int(egress.slot)); c.Packets != 2 || c.Bytes != 38 {
			t.Fatalf("default %d: counted %+v", def, c)
		}
	}
}

// TestPrefixKeyIsTheTriesKey checks the key layout the tries are filled
// with: the length as a host u32, then the address with its host bits
// cleared, whatever the prefix's spelling.
func TestPrefixKeyIsTheTriesKey(t *testing.T) {
	key := prefixKey(netip.MustParsePrefix("10.1.2.3/16"))
	//: 4 + 4 bytes.
	if len(key) != 8 || binary.NativeEndian.Uint32(key) != 16 || string(key[4:]) != "\x0a\x01\x00\x00" {
		t.Fatalf("IPv4 key % x", key)
	}
	key = prefixKey(netip.MustParsePrefix("fd00:1:2::/32"))
	//: 4 + 16 bytes.
	if len(key) != 20 || binary.NativeEndian.Uint32(key) != 32 || key[4] != 0xfd || key[8] != 0 {
		t.Fatalf("IPv6 key % x", key)
	}
	//: the EtherType as __sk_buff holds it reads back in network order.
	var b [2]byte
	binary.NativeEndian.PutUint16(b[:], uint16(wireEtherType(etherTypeIP6)))
	if binary.BigEndian.Uint16(b[:]) != etherTypeIP6 {
		t.Fatalf("wireEtherType(%#x) = %#x", etherTypeIP6, wireEtherType(etherTypeIP6))
	}
}
