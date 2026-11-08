package bpf

import "net/netip"

const (
	// AnyNumber is the wildcard of a device rule's Major or Minor: it matches
	// every number, as "*" does in a devices.allow line.
	AnyNumber int64 = -1
	// MaxDeviceRules bounds a DevicePolicyValue's rules: each one is a few
	// instructions of a single program, compiled in a straight line.
	MaxDeviceRules int = 256
	// MaxBindRules bounds a BindPolicyValue's rules, for the same reason.
	MaxBindRules int = 256
	// MaxPrefixRules bounds a SKBPolicyValue's rules: they become the entries
	// of two longest-prefix-match maps, one per address family.
	MaxPrefixRules int = 16384
)

// Verdict is what a rule, or a policy's default, decides. The zero is no
// verdict and is refused (ADR 0031): a policy that does not say whether it
// allows or denies is a mistake, not a choice.
type Verdict uint8

const (
	// Deny refuses: the device node is not opened, the packet is dropped, the
	// bind fails with EPERM.
	Deny Verdict = 1 + iota
	// Allow lets the operation through to the other programs attached to the
	// group and to its ancestors, which must allow it too.
	Allow
)

// DeviceType is the kind of device node a device rule matches. The zero is
// refused.
type DeviceType uint8

const (
	// DeviceBlock matches block devices (devices.allow's "b").
	DeviceBlock DeviceType = 1 + iota
	// DeviceChar matches character devices ("c").
	DeviceChar
	// DeviceAny matches both ("a").
	DeviceAny
)

// DeviceAccess is a set of accesses to a device node. A rule whose set is
// empty matches nothing and is refused.
type DeviceAccess uint8

const (
	// AccessMknod is creating the node ("m").
	AccessMknod DeviceAccess = 1 << iota
	// AccessRead is opening it for reading ("r").
	AccessRead
	// AccessWrite is opening it for writing ("w").
	AccessWrite
	// AccessAll is the three ("rwm").
	AccessAll = AccessMknod | AccessRead | AccessWrite
)

// Family is the address family a bind rule matches. The zero is refused.
type Family uint8

const (
	// FamilyIPv4 matches a bind of an AF_INET socket.
	FamilyIPv4 Family = 1 + iota
	// FamilyIPv6 matches a bind of an AF_INET6 socket.
	FamilyIPv6
	// FamilyAny matches both.
	FamilyAny
)

// Transport is the transport protocol a bind rule matches. The zero is
// refused.
type Transport uint8

const (
	// TransportTCP matches a TCP socket.
	TransportTCP Transport = 1 + iota
	// TransportUDP matches a UDP socket.
	TransportUDP
	// TransportAny matches every protocol, TCP and UDP included.
	TransportAny
)

// DeviceRuleValue is one line of a device policy, as devices.allow writes
// it: a type, a major and a minor number — AnyNumber for any —, the
// accesses it covers, and its verdict. An Allow rule matches a request whose
// every access it covers; a Deny rule matches a request one of whose
// accesses it covers. So a read-write open is not allowed by a rule that
// allows reading only, and it is denied by a rule that denies writing.
type DeviceRuleValue struct {
	// Type is the kind of node the rule matches.
	Type DeviceType
	// Major is the major number, 0 to 4294967295, or AnyNumber.
	Major int64
	// Minor is the minor number, 0 to 4294967295, or AnyNumber.
	Minor int64
	// Access is the set of accesses the rule covers; never empty.
	Access DeviceAccess
	// Verdict is the rule's decision when it matches.
	Verdict Verdict
}

// DevicePolicyValue is one cgroup_device program: its rules, tried in
// order, the first that matches deciding, and the verdict when none does.
type DevicePolicyValue struct {
	// Rules are tried in order; at most MaxDeviceRules.
	Rules []DeviceRuleValue
	// Default decides a request no rule matches.
	Default Verdict
}

// PrefixRuleValue is one prefix of an IP policy and its verdict: the
// verdict of every peer address the prefix holds, unless a longer prefix of
// the same policy holds it too.
type PrefixRuleValue struct {
	// Prefix is an IPv4 or an IPv6 prefix; an IPv4-mapped IPv6 prefix is
	// refused, since the packets it means are IPv4 on the wire.
	Prefix netip.Prefix
	// Verdict is the rule's decision for an address the prefix holds.
	Verdict Verdict
}

// SKBPolicyValue is a pair of cgroup_skb programs, one on ingress and one on
// egress. The peer's address — a received packet's source, a sent packet's
// destination — is looked up among Rules, and the longest prefix holding it
// decides; Default decides for an address no prefix holds and for a packet
// that is neither IPv4 nor IPv6. Each program counts what it sees.
type SKBPolicyValue struct {
	// Rules are the prefixes, each at most once; at most MaxPrefixRules.
	Rules []PrefixRuleValue
	// Default decides an address no prefix holds.
	Default Verdict
}

// PortRangeValue is the inclusive range of ports First to Last. {0, 0} is
// the bind that leaves the port to the kernel.
type PortRangeValue struct {
	// First is the lowest port of the range.
	First uint16
	// Last is the highest port of the range, never below First.
	Last uint16
}

// BindRuleValue is one rule of a bind policy: an address family, a
// transport, a range of ports, and its verdict.
type BindRuleValue struct {
	// Family is the address family the rule matches.
	Family Family
	// Transport is the protocol the rule matches.
	Transport Transport
	// Ports is the range of local ports the rule matches.
	Ports PortRangeValue
	// Verdict is the rule's decision when it matches.
	Verdict Verdict
}

// BindPolicyValue is a pair of cgroup_sock_addr programs, on the IPv4 and
// the IPv6 bind hooks: the rules are tried in order, the first that matches
// deciding, and Default decides a bind no rule matches.
type BindPolicyValue struct {
	// Rules are tried in order; at most MaxBindRules.
	Rules []BindRuleValue
	// Default decides a bind no rule matches.
	Default Verdict
}

// TrafficValue is what one cgroup_skb program counted: every IP packet it
// saw and their bytes, the network header included, and among them those it
// dropped. A packet another program dropped after this one passed it is
// counted as passed.
type TrafficValue struct {
	// Packets is the number of packets the program saw.
	Packets uint64
	// Bytes is the number of bytes of those packets.
	Bytes uint64
	// DroppedPackets is the number of packets the program dropped.
	DroppedPackets uint64
	// DroppedBytes is the number of bytes of those packets.
	DroppedBytes uint64
}

// CountersValue is what the two programs of a SKBProgram counted since they
// were attached: the receiving program's count and the sending one's, read
// together.
type CountersValue struct {
	// Ingress is the receiving program's count.
	Ingress TrafficValue
	// Egress is the sending program's count.
	Egress TrafficValue
}
