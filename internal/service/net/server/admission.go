package server

import (
	"context"
	stdnet "net"
	"sync"
	"sync/atomic"
)

// AdmissionConfig parameterises [NewAdmission]. A zero budget is clamped to
// its default, never read as "unlimited" (ADR 0031, clamp half).
type AdmissionConfig struct {
	// PerAddress caps the connections one IPv4 address, or one IPv6 /64, may
	// hold before their handshake completes. Default 4.
	PerAddress int
	// Reserved is the budget of the addresses declared with SetReserved.
	// Default 8.
	Reserved int
}

// Admission is the pre-handshake admission of ADR 0174 D2: a per-address
// budget of connections that have not completed their handshake, checked
// before any TLS byte is read, with a reserved budget for declared
// addresses so a flood from elsewhere never locks them out. It is safe for
// concurrent use, and shared by a [StreamGroup] ([PreHandshake]) and by any
// transport that runs its own listener.
type Admission struct {
	cfg      AdmissionConfig
	mu       sync.Mutex
	pending  map[string]int
	reserved map[string]bool
	refused  atomic.Int64
}

// NewAdmission returns an admission with no reserved address.
func NewAdmission(cfg AdmissionConfig) *Admission {
	if cfg.PerAddress <= 0 {
		cfg.PerAddress = 4
	}
	if cfg.Reserved <= 0 {
		cfg.Reserved = 8
	}
	return &Admission{cfg: cfg, pending: map[string]int{}, reserved: map[string]bool{}}
}

// admissionKey is the budget a remote address counts against: the IPv4
// address, or the IPv6 /64.
func admissionKey(a stdnet.Addr) string {
	var ip stdnet.IP
	switch t := a.(type) {
	case *stdnet.TCPAddr:
		ip = t.IP
	case *stdnet.UDPAddr:
		ip = t.IP
	default:
		return a.String()
	}
	return admissionKeyOf(ip)
}

func admissionKeyOf(ip stdnet.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(stdnet.CIDRMask(64, 128)).String() + "/64"
}

// Admit counts a new connection from remote against its address's budget.
// It returns ok false, counted, when the budget is spent: the caller closes
// the connection before reading a byte. Otherwise release, idempotent,
// gives the slot back once the handshake completes or the connection ends.
func (a *Admission) Admit(remote stdnet.Addr) (release func(), ok bool) {
	k := admissionKey(remote)
	a.mu.Lock()
	limit := a.cfg.PerAddress
	if a.reserved[k] {
		limit = a.cfg.Reserved
	}
	if a.pending[k] >= limit {
		a.mu.Unlock()
		a.refused.Add(1)
		return func() {}, false
	}
	a.pending[k]++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			if a.pending[k]--; a.pending[k] <= 0 {
				delete(a.pending, k)
			}
			a.mu.Unlock()
		})
	}, true
}

// SetReserved replaces the reserved addresses. Each is a host or a
// host:port, resolved now; one that does not resolve is given no reserved
// budget, never an unkeyed one.
func (a *Admission) SetReserved(addrs []string) {
	keys := map[string]bool{}
	for _, s := range addrs {
		host := s
		if h, _, err := stdnet.SplitHostPort(s); err == nil {
			host = h
		}
		ips, err := stdnet.LookupIP(host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			keys[admissionKeyOf(ip)] = true
		}
	}
	a.mu.Lock()
	a.reserved = keys
	a.mu.Unlock()
}

// Refused counts the connections refused before their handshake.
func (a *Admission) Refused() int64 { return a.refused.Load() }

// PreHandshake puts the group's accepted connections under a, before any
// TLS byte is read (ADR 0174 D2).
func PreHandshake(a *Admission) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.admission = a
	}
}

// admissionReleaseKey carries a connection's admission release to the
// handshake handler, which gives the slot back once the peer authenticated.
type admissionReleaseKey struct{}

// releaseAdmission gives a connection's admission slot back, if it has one.
func releaseAdmission(ctx context.Context) {
	if release, ok := ctx.Value(admissionReleaseKey{}).(func()); ok {
		release()
	}
}
