package service

import (
	"context"
	"os"
	"runtime"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// Instances per connection (S0f, kitsunium/sdk#740) are systemd.socket(5)'s
// Accept=yes: a unit of the template per accepted connection, the connection
// handed to its main process as the one socket of the sd_listen_fds protocol.
// The policy — how many, their names, the template — is the caller's.

// connectionName is what LISTEN_FDNAMES names an instance's connection, as
// systemd names an Accept=yes socket.
const connectionName string = "connection"

// SpawnInstance makes the unit of one accepted connection — systemd.socket(5)'s
// Accept=yes — and starts it: a copy of template named instance, which owns
// conn from the call on. Every ExecStart= process of it is handed conn at fd
// 3 — after the sockets template.Spec already activates —, LISTEN_FDS
// counting it, LISTEN_FDNAMES naming it "connection", LISTEN_PID the
// process's own; no other command gets it. The instance runs through the
// same reducer as any unit, Restart= included, and ends on its own once its
// run is over — dead, no job left, no restart due —: conn closed, its
// goroutines joined, every later call UNIT_CLOSED. The instance's name, the
// template and the limits on connections are the caller's.
//
// The unit is returned with Start's error once it was made: START_FAILED
// (the connection then closed, the unit ended, State saying why), or ctx's
// error, the start going on. A configuration NewUnit refuses, a nil conn
// (CONFIG_INVALID) or Windows (DIRECTIVE_UNSUPPORTED: no descriptor is
// handed there) return no unit, conn and template.Notify closed. A
// template.Notify is the instance's, as NewUnit takes it: each instance needs
// its own. So does a template.Cgroup that is a coresvc.CgroupUnit: its modes
// signal and watch the whole group, and instances sharing one would stop
// one another. A Type=dbus template is CONFIG_INVALID naming Accept: its
// instances would share one bus name, which systemd refuses to a second
// unit.
func SpawnInstance(ctx context.Context, template *Config, instance string, conn *os.File) (*Unit, error) {
	own := *template
	own.Name = instance
	u, err := newInstance(&own, conn)
	//: refused: nothing was made.
	if err != nil {
		//: the refusal.
		return nil, err
	}
	err = u.Start(ctx)
	//: the caller gave up — maybe before the start was even posted: it is
	//: posted again, a start joining a start, so the instance runs and ends.
	if err != nil && err == ctx.Err() {
		u.post(&event{kind: evStart})
	}
	//: the instance, and its start's answer.
	return u, err
}

// newInstance makes an instance's unit, inactive, owning conn; a refusal
// closes conn and the configuration's NotifySource.
func newInstance(cfg *Config, conn *os.File) (*Unit, error) {
	//: no connection, or nowhere to hand it.
	if refusal := instanceRefusal(conn); refusal != nil {
		closeRefused(cfg.Notify, conn)
		//: the refusal.
		return nil, refusal
	}
	s, err := resolve(cfg)
	//: a configuration the engine refuses — or a Type=dbus template, whose
	//: one bus name instances would share (systemd refuses a second unit on
	//: a name): the socket and the connection closed with it.
	if err == nil && s.typ == coresvc.TypeDbus {
		err = invalid("Accept", "Type=dbus")
	}
	//: refused.
	if err != nil {
		closeRefused(cfg.Notify, conn)
		//: the refusal, naming the directive.
		return nil, err
	}
	//: the instance, inactive.
	return launch(cfg, s, conn), nil
}

// closeRefused closes what a refused instance owned: its NotifySource and
// its connection.
func closeRefused(src coresvc.NotifySource, conn *os.File) {
	//: the socket.
	if src != nil {
		gone(src.Close())
	}
	//: the connection.
	if conn != nil {
		gone(conn.Close())
	}
}

// instanceRefusal refuses a missing connection, and Windows, where no
// descriptor is handed to a child (listen.PrepareFiles).
func instanceRefusal(conn *os.File) error {
	//: by the cause.
	switch {
	//: nothing to hand.
	case conn == nil:
		//: CONFIG_INVALID.
		return invalid("Accept", "no connection")
	//: no fd to hand it at.
	case runtime.GOOS == osWindows:
		//: DIRECTIVE_UNSUPPORTED.
		return unsupported("Accept", "yes", osWindows)
	//: a connection to hand.
	default:
		//: none.
		return nil
	}
}
