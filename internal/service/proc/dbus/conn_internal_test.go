package dbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The client over the fake bus: the authentication, Hello, the calls, the
// watch and its ordering, the bounds, a hang-up — every reply and signal
// written by the test's own encoder, in both byte orders.

const name = "org.example.Svc"

// field is an error's field value.
func field(err error, key string) string {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return ""
	}
	for _, f := range e.Fields() {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// recv reads one owner from a watch, failing after ten seconds.
func recv(t *testing.T, ch <-chan string) (string, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(10 * time.Second):
		t.Fatal("no owner within 10 s")
		return "", false
	}
}

// TestHandshake: the credentials byte, AUTH EXTERNAL with no identity, the
// empty DATA the bus asks for — the identity the kernel gives of the peer,
// which a client in a user namespace cannot misname —, BEGIN after OK, then
// Hello to the bus driver; its big-endian reply names the connection.
func TestHandshake(t *testing.T) {
	c, _ := pipeConn(t, ":1.42")
	if c.UniqueName() != ":1.42" {
		t.Fatalf("unique name %q", c.UniqueName())
	}
}

// TestAuthRefused: REJECTED is AUTH_REFUSED naming the mechanisms offered,
// whether the bus asked for the identity first or not; a line EXTERNAL never
// gets — a second DATA, one with a challenge —, or one past its bound, is
// PROTOCOL_VIOLATION.
func TestAuthRefused(t *testing.T) {
	for _, tc := range []struct {
		ask    bool
		answer string
		code   errs.Code
	}{
		{true, "REJECTED EXTERNAL DBUS_COOKIE_SHA1", coredbus.CodeAuthRefused},
		{false, "REJECTED EXTERNAL DBUS_COOKIE_SHA1", coredbus.CodeAuthRefused},
		{true, "DATA", coredbus.CodeProtocolViolation},
		{true, "DATA 6a6b", coredbus.CodeProtocolViolation},
		{false, "DATA 6a6b", coredbus.CodeProtocolViolation},
		{true, "OK" + strings.Repeat("x", 600), coredbus.CodeProtocolViolation},
	} {
		client, server := net.Pipe()
		b := &fakeBus{t: t, nc: server}
		b.r = bufio.NewReader(server)
		go func() { _ = b.acceptAs(tc.ask, tc.answer) }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := open(ctx, client)
		cancel()
		_ = server.Close()
		if !errs.HasCode(err, tc.code) {
			t.Errorf("%.20q: %v, want %x", tc.answer, err, tc.code)
		}
		if tc.code == coredbus.CodeAuthRefused && field(err, "mechanisms") != "EXTERNAL DBUS_COOKIE_SHA1" {
			t.Errorf("mechanisms %q", field(err, "mechanisms"))
		}
	}
}

// TestHandshakeBounded: a bus that never answers the authentication holds
// the caller no longer than its context's deadline (CALL_TIMEOUT, the
// deadline in the chain), its cancellation (CALL_CANCELLED), or — with
// neither — 25 seconds (CALL_TIMEOUT), here on a synctest bubble's clock.
func TestHandshakeBounded(t *testing.T) {
	silent := func() net.Conn {
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		go func() {
			buf := make([]byte, 64)
			_, _ = server.Read(buf)
		}()
		return client
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := open(ctx, silent()); !errs.HasCode(err, coredbus.CodeCallTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("past the deadline: %v, want CALL_TIMEOUT with the deadline in the chain", err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, ccancel)
	if _, err := open(cctx, silent()); !errs.HasCode(err, coredbus.CodeCallCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v, want CALL_CANCELLED", err)
	}
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		go func() {
			buf := make([]byte, 64)
			_, _ = server.Read(buf)
		}()
		start := time.Now()
		_, err := open(context.Background(), client)
		if !errs.HasCode(err, coredbus.CodeCallTimeout) || time.Since(start) != callTimeout {
			t.Fatalf("no deadline: %v after %s, want CALL_TIMEOUT after %s", err, time.Since(start), callTimeout)
		}
	})
}

// TestCallBounded: a call the bus never answers, with a context that has no
// deadline, is CALL_TIMEOUT after 25 seconds — sd-bus's default reply
// timeout —, here on a synctest bubble's clock; the connection goes on.
func TestCallBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, b := pipeConn(t, ":1.42")
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			_, err := c.NameOwner(context.Background(), name)
			done <- err
		}()
		b.next()
		err := <-done
		if !errs.HasCode(err, coredbus.CodeCallTimeout) || field(err, "method") != "GetNameOwner" || time.Since(start) != callTimeout {
			t.Fatalf("an unanswered call: %v after %s", err, time.Since(start))
		}
		out := make(chan string, 1)
		go func() {
			s, _ := c.NameOwner(context.Background(), name)
			out <- s
		}()
		b.reply(b.next(), binary.LittleEndian, "s", ":1.2")
		if s := <-out; s != ":1.2" {
			t.Fatalf("the next call read %q", s)
		}
		_ = c.Close()
	})
}

// TestWatchName: AddMatch on the name's NameOwnerChanged, then GetNameOwner;
// an announcement read before the lookup's reply is older than it and
// dropped, every one after it delivered in order; another sender's, or
// another name's, is never a change.
func TestWatchName(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan (<-chan string), 1)
	go func() {
		ch, err := c.WatchName(ctx, name)
		if err != nil {
			t.Errorf("WatchName: %v", err)
		}
		got <- ch
	}()
	add := b.next()
	if add.m.member != "AddMatch" || add.args[0] != matchRule(name) || !strings.Contains(add.args[0], "arg0='"+name+"'") {
		t.Fatalf("first call %s %q", add.m.member, add.args)
	}
	b.reply(add, binary.LittleEndian, "")
	lookup := b.next()
	if lookup.m.member != "GetNameOwner" || lookup.args[0] != name {
		t.Fatalf("second call %s %q", lookup.m.member, lookup.args)
	}
	b.ownerChanged(binary.LittleEndian, busDriver, name, "", ":1.5") // older than the lookup
	b.reply(lookup, binary.BigEndian, "s", ":1.7")
	ch := <-got
	b.ownerChanged(binary.BigEndian, ":1.66", name, ":1.7", "")               // not the bus driver
	b.ownerChanged(binary.LittleEndian, busDriver, "org.other.X", "", ":1.8") // another name
	b.ownerChanged(binary.LittleEndian, busDriver, name, ":1.7", "")
	b.ownerChanged(binary.BigEndian, busDriver, name, "", ":1.9")
	for _, want := range []string{":1.7", "", ":1.9"} {
		if v, ok := recv(t, ch); !ok || v != want {
			t.Fatalf("owner %q (open %v), want %q", v, ok, want)
		}
	}
	cancel()
	remove := b.next()
	if remove.m.member != "RemoveMatch" || remove.args[0] != matchRule(name) || remove.m.flags&flagNoReplyExpected == 0 {
		t.Fatalf("after the watch: %s %q flags %d", remove.m.member, remove.args, remove.m.flags)
	}
	if _, ok := recv(t, ch); ok {
		t.Fatal("the channel is still open after the watch's context ended")
	}
}

// TestWatchNameNoOwner: NameHasNoOwner is the empty owner, the watch's first.
func TestWatchNameNoOwner(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	got := make(chan (<-chan string), 1)
	go func() {
		ch, err := c.WatchName(context.Background(), name)
		if err != nil {
			t.Errorf("WatchName: %v", err)
		}
		got <- ch
	}()
	b.reply(b.next(), binary.LittleEndian, "")
	b.fail(b.next(), nameHasNoOwner)
	if v, ok := recv(t, <-got); !ok || v != "" {
		t.Fatalf("first owner %q (open %v), want none", v, ok)
	}
}

// TestWatchRefused: an AddMatch the bus refuses fails the watch with
// CALL_FAILED naming the error; a name that is not well-known never reaches
// the bus.
func TestWatchRefused(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	done := make(chan error, 1)
	go func() {
		_, err := c.WatchName(context.Background(), name)
		done <- err
	}()
	b.fail(b.next(), "org.freedesktop.DBus.Error.AccessDenied")
	err := <-done
	if !errs.HasCode(err, coredbus.CodeCallFailed) || field(err, "error") != "org.freedesktop.DBus.Error.AccessDenied" ||
		field(err, "method") != "AddMatch" {
		t.Fatalf("a refused AddMatch: %v", err)
	}
	if _, err := c.WatchName(context.Background(), ":1.7"); !errs.HasCode(err, coredbus.CodeNameInvalid) {
		t.Fatalf("a unique name: %v, want NAME_INVALID", err)
	}
}

// TestNames: RequestName never queues and reads its answer; ReleaseName
// too; NameOwner reads an owner or none; a refusal is CALL_FAILED.
func TestNames(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	ctx := context.Background()
	type result struct {
		ok  bool
		s   string
		err error
	}
	run := func(f func() result) chan result {
		out := make(chan result, 1)
		go func() { out <- f() }()
		return out
	}
	for _, tc := range []struct {
		reply uint32
		want  bool
	}{{1, true}, {4, true}, {3, false}, {2, false}} {
		out := run(func() result { ok, err := c.RequestName(ctx, name); return result{ok: ok, err: err} })
		req := b.next()
		if req.m.member != "RequestName" || req.args[0] != name || req.u != requestDoNotQueue || req.m.sig != "su" {
			t.Fatalf("RequestName sent %q %q %d", req.m.sig, req.args, req.u)
		}
		b.reply(req, binary.LittleEndian, "u", tc.reply)
		if r := <-out; r.err != nil || r.ok != tc.want {
			t.Errorf("RequestName answered %d: %v %v, want %v", tc.reply, r.ok, r.err, tc.want)
		}
	}
	out := run(func() result { ok, err := c.RequestName(ctx, name); return result{ok: ok, err: err} })
	b.fail(b.next(), "org.freedesktop.DBus.Error.AccessDenied")
	if r := <-out; !errs.HasCode(r.err, coredbus.CodeCallFailed) {
		t.Fatalf("a refused RequestName: %v", r.err)
	}
	for _, tc := range []struct {
		reply uint32
		want  bool
	}{{1, true}, {2, false}, {3, false}} {
		out := run(func() result { ok, err := c.ReleaseName(ctx, name); return result{ok: ok, err: err} })
		b.reply(b.next(), binary.BigEndian, "u", tc.reply)
		if r := <-out; r.err != nil || r.ok != tc.want {
			t.Errorf("ReleaseName answered %d: %v %v", tc.reply, r.ok, r.err)
		}
	}
	out = run(func() result { s, err := c.NameOwner(ctx, name); return result{s: s, err: err} })
	b.reply(b.next(), binary.LittleEndian, "s", ":1.3")
	if r := <-out; r.err != nil || r.s != ":1.3" {
		t.Fatalf("NameOwner: %q %v", r.s, r.err)
	}
	out = run(func() result { s, err := c.NameOwner(ctx, name); return result{s: s, err: err} })
	b.fail(b.next(), nameHasNoOwner)
	if r := <-out; r.err != nil || r.s != "" {
		t.Fatalf("NameOwner of nobody's: %q %v", r.s, r.err)
	}
	out = run(func() result { s, err := c.NameOwner(ctx, name); return result{s: s, err: err} })
	b.reply(b.next(), binary.LittleEndian, "u", uint32(1))
	if r := <-out; !errs.HasCode(r.err, coredbus.CodeProtocolViolation) {
		t.Fatalf("a reply of another signature: %v, want PROTOCOL_VIOLATION", r.err)
	}
}

// TestCallCancelled: a call whose context ends before its reply is
// CALL_CANCELLED, the context's error in the chain; the late reply is
// dropped and the connection goes on.
func TestCallCancelled(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.NameOwner(ctx, name)
		done <- err
	}()
	late := b.next()
	cancel()
	err := <-done
	if !errs.HasCode(err, coredbus.CodeCallCancelled) || !errors.Is(err, context.Canceled) || field(err, "method") != "GetNameOwner" {
		t.Fatalf("a cancelled call: %v", err)
	}
	b.reply(late, binary.LittleEndian, "s", ":1.1")
	out := make(chan string, 1)
	go func() {
		s, _ := c.NameOwner(context.Background(), name)
		out <- s
	}()
	b.reply(b.next(), binary.LittleEndian, "s", ":1.2")
	if s := <-out; s != ":1.2" {
		t.Fatalf("the next call read %q, want its own reply", s)
	}
}

// TestBodyPastTheBound: a body past 1 MiB is read and dropped, never
// allocated: the connection stays usable and the next reply is read.
func TestBodyPastTheBound(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	out := make(chan string, 1)
	go func() {
		s, _ := c.NameOwner(context.Background(), name)
		out <- s
	}()
	req := b.next()
	big := strings.Repeat("x", int(maxBodyBytes)+10)
	b.ownerChanged(binary.LittleEndian, busDriver, name, big, ":1.4")
	b.reply(req, binary.LittleEndian, "s", ":1.5")
	if s := <-out; s != ":1.5" {
		t.Fatalf("after a dropped body: %q", s)
	}
}

// TestProtocolViolation: a header the specification forbids — a byte order
// that is none, a field array past its bound, a string that is not UTF-8 —
// closes the connection: the call waiting is CONN_CLOSED, a watch's channel
// closed, every later call CONN_CLOSED.
func TestProtocolViolation(t *testing.T) {
	bad := map[string]func(b *fakeBus) []byte{
		"byte order": func(*fakeBus) []byte { return []byte{'X', 2, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0} },
		"field array": func(*fakeBus) []byte {
			p := []byte{'l', 2, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(p[12:], maxFieldBytes+8)
			return p
		},
		"utf-8": func(b *fakeBus) []byte {
			return b.encode(binary.LittleEndian, typeMethodReturn, 99, "org.\xff", "")
		},
	}
	for rule, msg := range bad {
		c, b := pipeConn(t, ":1.42")
		done := make(chan error, 1)
		go func() {
			_, err := c.NameOwner(context.Background(), name)
			done <- err
		}()
		b.next()
		b.write(msg(b))
		if err := <-done; !errs.HasCode(err, coredbus.CodeConnClosed) {
			t.Errorf("%s: the waiting call %v, want CONN_CLOSED", rule, err)
		}
		if _, err := c.NameOwner(context.Background(), name); !errs.HasCode(err, coredbus.CodeConnClosed) {
			t.Errorf("%s: a later call %v, want CONN_CLOSED", rule, err)
		}
		c.mu.Lock()
		cause := c.cause
		c.mu.Unlock()
		if !errs.HasCode(cause, coredbus.CodeProtocolViolation) {
			t.Errorf("%s: the connection ended for %v, want PROTOCOL_VIOLATION", rule, cause)
		}
	}
}

// TestBacklogCoalesces: a reader that falls behind keeps the oldest owners
// and the newest, the ones between coalesced: 64 at most.
func TestBacklogCoalesces(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	got := make(chan (<-chan string), 1)
	go func() {
		ch, err := c.WatchName(context.Background(), name)
		if err != nil {
			t.Errorf("WatchName: %v", err)
		}
		got <- ch
	}()
	b.reply(b.next(), binary.LittleEndian, "")
	b.reply(b.next(), binary.LittleEndian, "s", ":1.0")
	ch := <-got
	for i := 1; i <= 100; i++ {
		b.ownerChanged(binary.LittleEndian, busDriver, name, "", ":2."+strconv.Itoa(i))
	}
	synced := make(chan struct{})
	go func() {
		_, _ = c.NameOwner(context.Background(), name)
		close(synced)
	}()
	b.reply(b.next(), binary.LittleEndian, "s", ":2.100")
	<-synced
	var owners []string
	for range maxBacklog {
		v, _ := recv(t, ch)
		owners = append(owners, v)
	}
	if owners[0] != ":1.0" || owners[1] != ":2.1" || owners[62] != ":2.62" || owners[63] != ":2.100" {
		t.Fatalf("backlog %v", owners)
	}
}

// TestHangUp: the bus hanging up ends the connection: the calls waiting and
// later are CONN_CLOSED, every watch's channel is closed, Close joins.
func TestHangUp(t *testing.T) {
	c, b := pipeConn(t, ":1.42")
	got := make(chan (<-chan string), 1)
	go func() {
		ch, _ := c.WatchName(context.Background(), name)
		got <- ch
	}()
	b.reply(b.next(), binary.LittleEndian, "")
	b.reply(b.next(), binary.LittleEndian, "s", "")
	ch := <-got
	if v, ok := recv(t, ch); !ok || v != "" {
		t.Fatalf("first owner %q", v)
	}
	_ = b.nc.Close()
	if _, ok := recv(t, ch); ok {
		t.Fatal("the watch's channel is open after the hang-up")
	}
	if _, err := c.RequestName(context.Background(), name); !errs.HasCode(err, coredbus.CodeConnClosed) {
		t.Fatalf("a call after the hang-up: %v", err)
	}
	if _, err := c.WatchName(context.Background(), name); !errs.HasCode(err, coredbus.CodeConnClosed) {
		t.Fatalf("a watch after the hang-up: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
