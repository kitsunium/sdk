// Package datagram sends a backlog of datagrams over a connected socket in as
// few system calls as the kernel allows: sendmmsg(2) on Linux, up to MaxBatch
// datagrams a call, and one Write per datagram elsewhere (#807). It is what the
// udp writer and journald hand the records their drainer holds. Every payload
// stays ONE datagram — nothing is gathered — so a record is never merged with
// the next, which is the property the udp writer's and journald's framing rest
// on. It declares no error code: a sink wraps what Send returns under its own.
package datagram
