package net

// SteerValue names how a Steer group spreads its datagrams over its shards. The
// zero value, SteerHash, is the kernel's SO_REUSEPORT 4-tuple hash.
type SteerValue uint8

const (
	// SteerHash spreads a group's datagrams with the kernel's 4-tuple hash.
	SteerHash SteerValue = iota
	// SteerCPU hands each datagram to shard cpu mod n, the softirq's CPU
	// (SO_ATTACH_REUSEPORT_CBPF, Linux 4.5).
	SteerCPU
)
