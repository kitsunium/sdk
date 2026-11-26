//go:build linux

package consensus

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The safety review of #1482's first fix (D1): the TCP transport bounds a
// resend by its sender's timeouts, on its sender's running time. A sender
// stopped — SIGSTOP, a frozen cgroup, a paused machine — while a write of a
// batch blocks resumes past its deadline, dials again and writes the whole
// batch again: its peer, which ran all along, receives the copy of a
// forward as late as the stop lasted. Measured at the defaults: 25.1 s, past
// the 18 s the first fix kept an answer for, and the step function proposed
// the copy again.
//
// TestAStalledSendersLateCopyIsAppendedOnce plays it end to end on a real
// TCP transport at each end and a real step function at the receiver: the
// sender, a child process (this test binary run again), forwards a command
// in a batch the receiver holds its reader on; the parent stops the child
// mid-write, lets its kernel drain, resumes it past the forward window, and
// counts what the leader appended. The receiver's WriteTimeout is 1 s, so
// its window is 12 s, and the stop 14 s; the sender keeps the defaults its
// schedule is built on. It runs on Linux, whose kernel this schedule was
// measured on; elsewhere the sockets' buffers differ.

// stallRoleEnv names the child's role: the sender, which spawns nothing.
const stallRoleEnv = "KITSUNIUM_CONSENSUS_STALL_ROLE"

// stallCtx is the forward's submission id, of the epoch 7, counter 9.
const stallCtx uint64 = 7<<32 | 9

// stallFor is how long the sender stays stopped: past the receiver's
// forward window of 12 s.
const stallFor = 14 * time.Second

// stallCommand is the command the sender forwards.
var stallCommand = []byte("withdraw 100")

// stallBig is a 12 MiB append, a frame larger than a socket's buffers.
func stallBig(i uint64) []byte {
	d := bytes.Repeat([]byte{byte(i)}, 12<<20)
	return appendFrame(&coreconsensus.MessageValue{
		Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 1,
		Entries: []coreconsensus.EntryValue{{Position: coreconsensus.PositionValue{Term: 1, Index: i}, Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d}},
	})
}

// stallForward is the sender's forward of stallCommand at term.
func stallForward(term uint64) []byte {
	return appendFrame(&coreconsensus.MessageValue{
		Version: 1, Group: 1, Kind: coreconsensus.MsgForward, From: 1, To: 2, Term: term, Context: stallCtx,
		Entries: []coreconsensus.EntryValue{{Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(stallCommand), Data: stallCommand}},
	})
}

// TestStalledSenderChild is the sender; it runs only as the child of
// TestAStalledSendersLateCopyIsAppendedOnce, never spawns, and ends within
// a minute whatever became of its parent.
func TestStalledSenderChild(t *testing.T) {
	if os.Getenv(stallRoleEnv) != "sender" {
		t.Skip("the sender of TestAStalledSendersLateCopyIsAppendedOnce, run only as its child")
	}
	dir := os.Getenv("STALL_DIR")
	read := func(name string) []byte { return must(os.ReadFile(filepath.Join(dir, name)))(t) }
	pair := must(tls.X509KeyPair(read("a.crt"), read("a.key")))(t)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(read("ca.crt")) {
		t.Fatal("ca")
	}
	a := must(NewTCPTransport(&TransportConfig{Certificate: &pair, Roots: pool, ListenAddress: "127.0.0.1:0"}))(t)
	defer func() { abandon(a.Close()) }()
	var aSPKI, bSPKI [32]byte
	copy(aSPKI[:], must(hex.DecodeString(os.Getenv("STALL_A_SPKI")))(t))
	copy(bSPKI[:], must(hex.DecodeString(os.Getenv("STALL_B_SPKI")))(t))
	term := must(strconv.ParseUint(os.Getenv("STALL_TERM"), 10, 64))(t)
	a.SetMembership(1, []coreconsensus.MemberValue{
		{ID: 1, Slot: "a", SPKI: aSPKI, Address: a.Addr(), Voter: true},
		{ID: 2, Slot: "b", SPKI: bSPKI, Address: os.Getenv("STALL_B_ADDR"), Voter: true},
	})
	// The link is dialled and written once, so the batch below is written
	// on a reused connection: sendBatch's redial-and-resend path.
	a.Send(2, appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 1}))
	time.Sleep(500 * time.Millisecond)
	// X1 is held by the receiver for 1.5 s once read: X2's write blocks
	// behind it, within its 2 s deadline, and the forward and the fillers,
	// queued meanwhile, leave as one batch, the forward first.
	x1, x2, f := stallBig(1), stallBig(2), stallForward(term)
	fill := [][]byte{stallBig(3), stallBig(4), stallBig(5)}
	a.Send(2, x1)
	time.Sleep(100 * time.Millisecond)
	a.Send(2, x2)
	time.Sleep(300 * time.Millisecond)
	a.Send(2, f)
	for _, y := range fill {
		a.Send(2, y)
	}
	time.Sleep(60 * time.Second)
}

// stallLeader is node 2, the one voter of a group whose node 1, the sender,
// is a learner: the leader of term 2 once it campaigned, its writes durable
// at once. Its clock is the test's virtual one.
type stallLeader struct {
	mu       sync.Mutex
	r        *raft.Raft
	appended int
}

// drain takes r's Ready until it is empty, every write durable at once,
// and counts the entries that carry stallCommand.
func (l *stallLeader) drain() {
	for i := 0; i < 64 && l.r.HasReady(); i++ {
		rd := l.r.Ready()
		for j := range rd.Entries {
			if e := &rd.Entries[j]; e.Kind == coreconsensus.EntryCommand && bytes.Equal(e.Data, stallCommand) {
				l.appended++
			}
		}
		if rd.NeedsWrite() {
			var idx, term uint64
			if n := len(rd.Entries); n > 0 {
				idx, term = rd.Entries[n-1].Position.Index, rd.Entries[n-1].Position.Term
			}
			l.r.StorageDone(rd.Seq, idx, term)
		}
	}
}

// step hands the frame to the leader at now.
func (l *stallLeader) step(now time.Time, frame []byte) {
	var m coreconsensus.MessageValue
	if wire.ParseMessage(bytes.Clone(frame), &m) != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.r.Step(now, m)
	l.drain()
}

// tick ticks the leader at now.
func (l *stallLeader) tick(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.r.Tick(now)
	l.drain()
}

// newStallLeader is node 2 elected at term 2 from the instant at, over the
// window the transport tr states (forwardWindow).
func newStallLeader(t *testing.T, tr *TCPTransport, aSPKI, bSPKI [32]byte, at time.Time) *stallLeader {
	t.Helper()
	tm := coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01,
	}
	conf := coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{
		{ID: 1, Slot: "a", SPKI: aSPKI}, {ID: 2, Slot: "b", SPKI: bSPKI, Voter: true},
	}}
	l := &stallLeader{r: raft.NewRaft(&raft.Config{
		ID: 2, Group: 1, Seed: 1, Timing: tm, MaxInflight: 8, MaxInflightBytes: 1 << 20, MaxBatchBytes: 1 << 16,
		MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, ForwardWindow: forwardWindow(tr),
	}, &raft.StartState{
		HardState: coreconsensus.HardState{Term: 1, Commit: 1},
		Snapshot: coreconsensus.SnapshotMetaValue{
			Position: coreconsensus.PositionValue{Term: 1, Index: 1}, Membership: conf, Timing: tm,
		},
		Now: at,
	})}
	for i := 1; i <= 50 && l.r.Status().Role != coreconsensus.RoleLeader; i++ {
		l.tick(at.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	if st := l.r.Status(); st.Role != coreconsensus.RoleLeader {
		t.Fatalf("setup: node 2 did not lead its group of one voter: %+v", st)
	}
	return l
}

// stallEnviron is this process's environment for the child: the role set
// once, any inherited value of it removed first, and without Bazel's
// sharding variables, under which the child would find its one test in
// another shard's share and run nothing.
func stallEnviron(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, stallRoleEnv+"=") || strings.HasPrefix(kv, "TEST_SHARD_") || strings.HasPrefix(kv, "TEST_TOTAL_SHARDS=")
	})
	return append(append(env, stallRoleEnv+"=sender"), extra...)
}

func TestAStalledSendersLateCopyIsAppendedOnce(t *testing.T) {
	if os.Getenv(stallRoleEnv) != "" {
		t.Skip("a child of this test spawns nothing")
	}
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	aCert, aSPKI := ca.issue(t, SAN(cid, "a"), nil)
	b := &tcpNode{id: 2, slot: "b", transport: func(c *TransportConfig) { c.WriteTimeout = time.Second }}
	b.cert, b.spki = ca.issue(t, SAN(cid, "b"), nil)
	b.listen(t, ca)
	defer func() { abandon(b.tr.Close()) }()
	window := 2 * b.tr.resendWindow()
	if stallFor <= window {
		t.Fatalf("setup: a stop of %v is within the receiver's forward window of %v", stallFor, window)
	}

	// The leader's clock: virtual before the transport runs, then the
	// monotonic time since, so the table ages as the test runs.
	t0 := time.Unix(1_800_000_000, 0)
	l := newStallLeader(t, b.tr, aSPKI, b.spki, t0)
	term := l.r.Status().Term
	start := time.Now()
	clk := func() time.Time { return t0.Add(10*time.Second + time.Since(start)) }
	stopTick := make(chan struct{})
	var ticks sync.WaitGroup
	ticks.Go(func() {
		for {
			select {
			case <-stopTick:
				return
			case <-time.After(100 * time.Millisecond):
				l.tick(clk())
			}
		}
	})
	defer func() { close(stopTick); ticks.Wait() }()

	var mu sync.Mutex
	var arrivals []time.Time
	gotF := make(chan struct{}, 8)
	release := make(chan struct{})
	var held, xHeld atomic.Bool
	if err := b.tr.Bind(1, func(_ coreconsensus.AuthenticatedPeerValue, frame []byte) {
		var m coreconsensus.MessageValue
		if wire.ParseMessage(bytes.Clone(frame), &m) != nil {
			return
		}
		if m.Kind == coreconsensus.MsgApp && len(m.Entries) == 1 && m.Entries[0].Position.Index == 1 && xHeld.CompareAndSwap(false, true) {
			// X1 is read: hold the reader while the forward and the
			// fillers queue behind it, so they leave as one batch.
			time.Sleep(1500 * time.Millisecond)
			return
		}
		if m.Kind != coreconsensus.MsgForward || m.Context != stallCtx {
			return
		}
		l.step(clk(), frame)
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		gotF <- struct{}{}
		if held.CompareAndSwap(false, true) {
			<-release
		}
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	b.tr.SetMembership(1, []coreconsensus.MemberValue{
		{ID: 1, Slot: "a", SPKI: aSPKI, Address: "127.0.0.1:9", Voter: true},
		{ID: 2, Slot: "b", SPKI: b.spki, Address: b.addr, Voter: true},
	})

	dir := t.TempDir()
	keyDER := must(x509.MarshalECPrivateKey(aCert.PrivateKey.(*ecdsa.PrivateKey)))(t)
	for name, blk := range map[string]*pem.Block{
		"ca.crt": {Type: "CERTIFICATE", Bytes: ca.cert.Raw},
		"a.crt":  {Type: "CERTIFICATE", Bytes: aCert.Certificate[0]},
		"a.key":  {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(blk), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestStalledSenderChild$", "-test.v", "-test.count=1")
	cmd.Env = stallEnviron("STALL_DIR="+dir, "STALL_B_ADDR="+b.addr, "STALL_TERM="+strconv.FormatUint(term, 10),
		"STALL_A_SPKI="+hex.EncodeToString(aSPKI[:]), "STALL_B_SPKI="+hex.EncodeToString(b.spki[:]))
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		abandon(cmd.Process.Signal(syscall.SIGCONT))
		abandon(cmd.Process.Kill())
		abandon(cmd.Wait())
	}()

	select {
	case <-gotF:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatalf("the forward never arrived; child: %s", out.String())
	}
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	stopped := time.Now()
	time.Sleep(100 * time.Millisecond)
	close(release) // the receiver reads on: the sender's kernel drains, its process stays stopped
	time.Sleep(stallFor)
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gotF:
	case <-time.After(20 * time.Second):
		// The schedule delivered no copy: the kernel drained the whole
		// batch before the stop, and nothing was written again.
		t.Skipf("no copy after the sender resumed, nothing to judge; child: %s", out.String())
	}
	mu.Lock()
	gap := arrivals[1].Sub(arrivals[0])
	mu.Unlock()
	t.Logf("sender stopped %v after the first delivery, for %v; the copy came %v after it (forward window %v)",
		stopped.Sub(arrivals[0]), stallFor, gap, window)
	l.mu.Lock()
	appended := l.appended
	l.mu.Unlock()
	if gap < window {
		t.Fatalf("the copy came %v after its original, within the window of %v: the schedule tests nothing past it", gap, window)
	}
	if appended != 1 {
		t.Fatalf("the leader appended the command %d times: its copy, %v late, past the window of %v, was proposed again", appended, gap, window)
	}
}
