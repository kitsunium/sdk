package server_test

import (
	stdnet "net"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestAdmissionBudgetsPerAddress pins ADR 0174 D2 on the value itself: a
// spent budget refuses and counts, a release gives the slot back once, and
// a reserved address has the larger budget.
func TestAdmissionBudgetsPerAddress(t *testing.T) {
	t.Parallel()
	a := server.NewAdmission(server.AdmissionConfig{PerAddress: 2, Reserved: 3})
	v4 := &stdnet.TCPAddr{IP: stdnet.ParseIP("192.0.2.7"), Port: 1}
	r1, ok1 := a.Admit(v4)
	_, ok2 := a.Admit(v4)
	if _, ok3 := a.Admit(v4); !ok1 || !ok2 || ok3 {
		t.Fatalf("budget of 2: %v %v %v", ok1, ok2, ok3)
	}
	if a.Refused() != 1 {
		t.Fatalf("refused %d, want 1", a.Refused())
	}
	r1()
	r1() // idempotent: one slot back, not two
	if _, ok := a.Admit(v4); !ok {
		t.Fatal("a released slot was not given back")
	}
	if _, ok := a.Admit(v4); ok {
		t.Fatal("a double release gave two slots back")
	}
	// Two IPv6 addresses of one /64 share a budget.
	x := &stdnet.TCPAddr{IP: stdnet.ParseIP("2001:db8::1")}
	y := &stdnet.TCPAddr{IP: stdnet.ParseIP("2001:db8::ffff")}
	a.Admit(x)
	a.Admit(y)
	if _, ok := a.Admit(x); ok {
		t.Fatal("an IPv6 /64 was not one budget")
	}
	a.SetReserved([]string{"192.0.2.7:4646"})
	if _, ok := a.Admit(v4); !ok {
		t.Fatal("a reserved address did not get its larger budget")
	}
}

// TestPreHandshakeRefusesBeforeTheHandler pins the option on a group: a
// connection over its address's budget is closed before it is served.
func TestPreHandshakeRefusesBeforeTheHandler(t *testing.T) {
	t.Parallel()
	a := server.NewAdmission(server.AdmissionConfig{PerAddress: 2})
	_, addr := startEcho(t, server.PreHandshake(a))
	var held []stdnet.Conn
	for range 2 {
		c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for a.Refused() == 0 {
		c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		_, rerr := c.Read(b[:])
		_ = c.Close()
		if rerr == nil {
			t.Fatal("a connection over the budget was served")
		}
		if time.Now().After(deadline) {
			t.Fatal("no refusal counted")
		}
	}
}
