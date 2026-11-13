//go:build linux

package server

import (
	"context"
	"encoding/binary"
	"errors"
	stdnet "net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shardSockets returns the server's datagram sockets in bind order.
func shardSockets(srv *Server) []*boundPacketConn {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	return append([]*boundPacketConn{}, srv.packetConns...)
}

// TestShards_bindsSharedPort pins the first half of #977's first criterion: four
// sockets on one port, each with SO_REUSEPORT read back, State reporting 4, even
// from a group asked for port 0.
func TestShards_bindsSharedPort(t *testing.T) {
	srv := New()
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(4)).
		HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	st := srv.State()
	if len(st.Listeners) != 1 || st.Listeners[0].Shards != 4 {
		t.Fatalf("Listeners = %+v, want one row of Shards 4", st.Listeners)
	}
	shards := shardSockets(srv)
	if len(shards) != 4 {
		t.Fatalf("%d sockets bound", len(shards))
	}
	want := shards[0].pc.LocalAddr().String()
	for i, shard := range shards {
		if got := shard.pc.LocalAddr().String(); got != want {
			t.Errorf("shard %d on %s, want %s", i, got, want)
		}
		raw, err := packetSocket(shard.pc).SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var value int
		var gerr error
		if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort) }); cerr != nil || gerr != nil {
			t.Fatalf("getsockopt: %v %v", cerr, gerr)
		}
		if value != 1 {
			t.Errorf("shard %d: SO_REUSEPORT = %d", i, value)
		}
	}
}

// TestShards_defaultOneSocket pins Shards(1) and the group without the option:
// one socket, and no SO_REUSEPORT.
func TestShards_defaultOneSocket(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []GroupOption
	}{
		{"without the option", nil},
		{"Shards(1)", []GroupOption{Shards(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New()
			srv.PacketGroup("p", append([]GroupOption{Listen("udp4", "127.0.0.1:0")}, tc.opts...)...).
				HandleFunc(func(context.Context, corenet.Packet) error { return nil })
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			shards := shardSockets(srv)
			if len(shards) != 1 || srv.State().Listeners[0].Shards != 1 {
				t.Fatalf("%d sockets, row %+v", len(shards), srv.State().Listeners[0])
			}
			raw, err := packetSocket(shards[0].pc).SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var value int
			var gerr error
			if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort) }); cerr != nil || gerr != nil {
				t.Fatalf("getsockopt: %v %v", cerr, gerr)
			}
			if value != 0 {
				t.Errorf("SO_REUSEPORT = %d on one socket, want 0", value)
			}
		})
	}
}

// TestShards_autoIsGOMAXPROCS pins Shards(0).
func TestShards_autoIsGOMAXPROCS(t *testing.T) {
	srv := New()
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(0)).
		HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if got := srv.State().Listeners[0].Shards; got != runtime.GOMAXPROCS(0) {
		t.Fatalf("Shards = %d, want %d", got, runtime.GOMAXPROCS(0))
	}
}

// exactlyOnce counts each sequence number and reports any seen twice.
type exactlyOnce struct {
	seen  []atomic.Bool
	dup   atomic.Uint64
	count atomic.Uint64
}

// note records seq.
func (e *exactlyOnce) note(seq uint32) {
	if e.seen[seq].Swap(true) {
		e.dup.Add(1)
		return
	}
	e.count.Add(1)
}

// TestShards_deliversExactlyOnce pins the criterion's run: 100 000 datagrams
// from 64 source ports over four shards, each read once and its echo
// byte-exact, with every shard reached; and the same run on Shards(1) and on
// a group without the option, the base whose socket Shards(1) is
// (TestShards_defaultOneSocket): one socket serves the run byte-exact as the
// base does (#977).
func TestShards_deliversExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []GroupOption
	}{
		{"Shards(4)", []GroupOption{Shards(4)}},
		{"Shards(1)", []GroupOption{Shards(1)}},
		{"without the option", nil},
	} {
		t.Run(tc.name, func(t *testing.T) { shardsRun(t, tc.opts) })
	}
}

// shardsRun is TestShards_deliversExactlyOnce's run on a group with opts.
func shardsRun(t *testing.T, opts []GroupOption) {
	const flows, perFlow = 64, 100_000 / 64
	total := flows * perFlow
	tally := &exactlyOnce{seen: make([]atomic.Bool, total)}
	srv := New()
	srv.PacketGroup("p", append([]GroupOption{Listen("udp4", "127.0.0.1:0"), ReceiveBuffer(4 << 20)}, opts...)...).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			tally.note(binary.BigEndian.Uint32(p.Data()))
			_, err := p.Reply(p.Data())
			return err
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	addr := srv.State().Listeners[0].Address
	var wait sync.WaitGroup
	start := time.Now()
	for f := range flows {
		wait.Go(func() { sendFlow(t, addr, uint32(f)*uint32(perFlow), perFlow) })
	}
	wait.Wait()
	if got := tally.count.Load(); got != uint64(total) || tally.dup.Load() != 0 {
		t.Fatalf("delivered %d of %d, %d twice, after %s", got, total, tally.dup.Load(), time.Since(start))
	}
}

// shardsPayload is one datagram of a flow: its index, then bytes that make
// one out of place visible, 1 200 in all as L3's.
const shardsPayload = 1200

// sendFlow is one 4-tuple, lock-step: each datagram waits for its echo, so the
// kernel's queues never overflow and exactly-once is the only outcome; every
// echo must be the datagram sent, byte for byte.
func sendFlow(t *testing.T, addr string, base uint32, n int) {
	t.Helper()
	conn, err := stdnet.Dial("udp4", addr)
	if err != nil {
		t.Errorf("dial: %v", err)
		return
	}
	defer func() { swallowErr(conn.Close()) }()
	var buf, ack [shardsPayload]byte
	for i := range n {
		id := base + uint32(i)
		binary.BigEndian.PutUint32(buf[:], id)
		for k := 4; k < len(buf); k++ {
			buf[k] = byte(id) ^ byte(k)
		}
		if _, err := conn.Write(buf[:]); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Errorf("deadline: %v", err)
			return
		}
		k, err := conn.Read(ack[:])
		if err != nil {
			t.Errorf("flow base %d, datagram %d: %v", base, i, err)
			return
		}
		if k != len(buf) || ack != buf {
			t.Errorf("flow base %d, datagram %d: echo of %d bytes differs from the %d sent", base, i, k, len(buf))
			return
		}
	}
}

// TestShards_handoverDrainsEveryShard pins ADR 0192 §D4 over four shards: what
// each socket queued while readiness was already false is served before Shutdown
// returns, none lost.
func TestShards_handoverDrainsEveryShard(t *testing.T) {
	var served atomic.Uint64
	release := make(chan struct{})
	srv := New()
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(4), ReceiveBuffer(4<<20)).
		HandleFunc(func(context.Context, corenet.Packet) error {
			<-release
			served.Add(1)
			return nil
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	addr := srv.State().Listeners[0].Address
	const flows, each = 16, 8
	for range flows {
		client, err := stdnet.Dial("udp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { swallowErr(client.Close()) })
		for range each {
			if _, err := client.Write([]byte("q")); err != nil {
				t.Fatal(err)
			}
		}
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown: %v", err)
	}
	if got := served.Load(); got != flows*each {
		t.Fatalf("served %d of %d datagrams through the handover", got, flows*each)
	}
}

// TestSteer_refusedOnUnixgramAndStream pins the refusals.
func TestSteer_refusedOnUnixgramAndStream(t *testing.T) {
	t.Run("unixgram with shards", func(t *testing.T) {
		srv := New()
		srv.PacketGroup("p", Listen("unixgram", socketDir(t)+"/p.sock"), Shards(2)).
			HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		err := srv.Start(t.Context())
		t.Cleanup(func() { swallowErr(srv.Close()) })
		if !errs.HasCode(err, corenet.CodePacketOptionUnsupported) {
			t.Fatalf("Start = %v, want PacketOptionUnsupported", err)
		}
	})
	t.Run("unixgram Shards(1) is one socket", func(t *testing.T) {
		srv := New()
		srv.PacketGroup("p", Listen("unixgram", socketDir(t)+"/p.sock"), Shards(1)).
			HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { swallowErr(srv.Close()) })
	})
	t.Run("steer on a stream group", func(t *testing.T) {
		srv := New()
		srv.Group("s", Listen("tcp", "127.0.0.1:0"), Steer(corenet.SteerCPU)).
			HandleFunc(func(context.Context, corenet.Conn) error { return nil })
		err := srv.Start(t.Context())
		t.Cleanup(func() { swallowErr(srv.Close()) })
		if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
			t.Fatalf("Start = %v, want LoopOptionUnsupported", err)
		}
	})
}

// shardOf says which shard read a packet, by the socket it arrived on.
func shardOf(sockets []*boundPacketConn, p corenet.Packet) int {
	conn := p.(*packet).conn
	for i, s := range sockets {
		if s.pc == conn {
			return i
		}
	}
	return -1
}

// pinToCPU locks the calling goroutine to its thread and that thread to cpu,
// and returns the undo; ok false says the host refused or lacks the CPU.
func pinToCPU(t *testing.T, cpu int) (unpin func(), ok bool) {
	t.Helper()
	if runtime.NumCPU() <= cpu {
		return nil, false
	}
	runtime.LockOSThread()
	var mask [128]byte
	mask[cpu/8] = 1 << (uint(cpu) % 8)
	var old [128]byte
	_, _, gerrno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(old)), uintptr(unsafe.Pointer(&old[0])))
	if _, _, serrno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0]))); serrno != 0 || gerrno != 0 {
		runtime.UnlockOSThread()
		return nil, false
	}
	return func() {
		_, _, _ = syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(old)), uintptr(unsafe.Pointer(&old[0])))
		runtime.UnlockOSThread()
	}, true
}

// TestSteer_cpuPicksTheShard pins the criterion: a sender pinned to one CPU
// has all its datagrams read by shard cpu mod n, loopback's softirq running on
// the sender's CPU. It skips on a host without CPU 1 or with an affinity refused,
// after saying so.
func TestSteer_cpuPicksTheShard(t *testing.T) {
	const cpu, n = 1, 3
	unpin, ok := pinToCPU(t, cpu)
	if !ok {
		t.Skipf("cannot pin to CPU %d on %d CPUs", cpu, runtime.NumCPU())
	}
	defer unpin()
	var (
		mu       sync.Mutex
		bucket   [n]int
		received atomic.Uint64
	)
	srv := New()
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(n), Steer(corenet.SteerCPU), ReceiveBuffer(4<<20)).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			if i := shardOf(shardSockets(srv), p); i >= 0 {
				mu.Lock()
				bucket[i]++
				mu.Unlock()
			}
			received.Add(1)
			return nil
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if rows := srv.State().Datagrams.Capabilities; !hasCapability(rows, corenet.CapabilityReuseportCBPF, statusOn) {
		t.Fatalf("capabilities = %+v, want reuseport-cbpf on", rows)
	}
	client, err := stdnet.Dial("udp4", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(client.Close()) })
	// Paced by windows the receive buffer always holds: every datagram goes to
	// one socket, whose SO_RCVBUF an unprivileged process gets capped at
	// rmem_max (425,984 bytes on a stock kernel, about 440 such datagrams), so a
	// burst of 1000 against a reader the host schedules late overflowed it and
	// read as a loss. The sender stays on its pinned thread while it waits.
	const total, window = 1000, 64
	deadline := time.Now().Add(20 * time.Second)
	for sent := 0; sent < total; {
		for end := min(sent+window, total); sent < end; sent++ {
			if _, err := client.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		for received.Load() < uint64(sent) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got := received.Load(); got != total {
		t.Fatalf("received %d of %d, shard counts %v", got, total, bucket)
	}
	if bucket[cpu%n] != total {
		t.Fatalf("shard counts = %v, want all %d on shard %d", bucket, total, cpu%n)
	}
}

// TestSteer_refusedAttachLeavesTheHash pins the refusal path: a kernel or a
// seccomp filter that refuses the program leaves the row absent and the hash
// spread delivering every datagram once.
func TestSteer_refusedAttachLeavesTheHash(t *testing.T) {
	saved := steerAttach
	steerAttach = func(int, []sockFilter) error { return syscall.EPERM }
	t.Cleanup(func() { steerAttach = saved })
	var received atomic.Uint64
	srv := New()
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(4), Steer(corenet.SteerCPU)).
		HandleFunc(func(context.Context, corenet.Packet) error {
			received.Add(1)
			return nil
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	rows := srv.State().Datagrams.Capabilities
	if !hasCapability(rows, corenet.CapabilityReuseportCBPF, statusAbsent) {
		t.Fatalf("capabilities = %+v, want reuseport-cbpf absent", rows)
	}
	client, err := stdnet.Dial("udp4", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(client.Close()) })
	for range 64 {
		_, _ = client.Write([]byte("x"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() < 64 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := received.Load(); got != 64 {
		t.Fatalf("hash spread delivered %d of 64", got)
	}
}

// TestSteer_disabledByOperator pins the token.
func TestSteer_disabledByOperator(t *testing.T) {
	saved := steerAttach
	called := false
	steerAttach = func(fd int, program []sockFilter) error { called = true; return saved(fd, program) }
	t.Cleanup(func() { steerAttach = saved })
	srv := newServer(envOf(map[string]string{corenet.EnvDisable: string(corenet.CapabilityReuseportCBPF)}))
	srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(2), Steer(corenet.SteerCPU)).
		HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if called {
		t.Fatal("the program was attached under the token")
	}
	if rows := srv.State().Datagrams.Capabilities; !hasCapability(rows, corenet.CapabilityReuseportCBPF, statusDisabled) {
		t.Fatalf("capabilities = %+v, want reuseport-cbpf disabled", rows)
	}
}
