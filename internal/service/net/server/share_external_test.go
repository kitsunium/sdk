// Package server_test — an address another process holds (#1330): refused
// under the default options, shared only on request.
package server_test

import (
	"bufio"
	"context"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// The second process of these tests is this test binary again, playing a
// holder: it starts a server on a loopback address, prints the address it
// bound and serves until its standard input closes or its lifetime ends. The
// guard is the one proc/service's helpers follow: the parent takes the role
// and the depth out of the environment it inherited before it sets the role,
// and a holder writes its depth into its own environment, so a holder that
// started another test binary would hand it the depth and that one would
// refuse — a holder never starts a holder.
const (
	// holderRoleEnv names the holder a child plays.
	holderRoleEnv = "KITSUNIUM_NET_SHARE_HOLDER"
	// holderDepthEnv is set in a holder's own environment.
	holderDepthEnv = "KITSUNIUM_NET_SHARE_DEPTH"
	// holderRefusal is the exit status of a holder the guard refused.
	holderRefusal = 97
	// holderLifetime bounds a holder's life, whatever became of its parent.
	holderLifetime = 60 * time.Second
	// holderWait bounds the parent's wait for the holder's address, and for
	// its exit once told to stop.
	holderWait = 20 * time.Second
	// childAnswer and parentAnswer are what each side's handler writes.
	childAnswer, parentAnswer = "child", "parent"
)

// holderRoles are the holders a child may play: the server each starts on
// 127.0.0.1:0.
var holderRoles = map[string]func(*server.Server){
	//: the default options: shards where the platform has SO_REUSEPORT.
	"stream-default": func(srv *server.Server) {
		srv.Group("held", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(answerWith(childAnswer))
	},
	//: the opt-in, for a progressive restart.
	"stream-share": func(srv *server.Server) {
		srv.Group("held", server.Listen("tcp", "127.0.0.1:0"), server.ShareAddress()).
			HandleFunc(answerWith(childAnswer))
	},
	//: a datagram group's explicit shards.
	"packet-shards": func(srv *server.Server) {
		srv.PacketGroup("held", server.Listen("udp", "127.0.0.1:0"), server.Shards(2)).HandleFunc(noopPacket)
	},
}

// answerWith is a stream handler that writes who it is and closes.
func answerWith(who string) func(context.Context, corenet.Conn) error {
	return func(_ context.Context, c corenet.Conn) error {
		_, err := io.WriteString(c, who+"\n")
		return err
	}
}

// runHolderIfAsked plays the holder this process was started as, and ends the
// process; it returns when the process is the test binary's own run.
func runHolderIfAsked() {
	role, asked := os.LookupEnv(holderRoleEnv)
	if !asked {
		//: a holder's depth without a role is a holder's child that lost
		//: its role: it must not run the suite again.
		if _, deep := os.LookupEnv(holderDepthEnv); deep {
			os.Exit(holderRefusal)
		}
		return
	}
	declare, known := holderRoles[role]
	_, deep := os.LookupEnv(holderDepthEnv)
	if !known || deep {
		os.Exit(holderRefusal)
	}
	if os.Setenv(holderDepthEnv, "1") != nil {
		os.Exit(holderRefusal)
	}
	os.Exit(hold(declare))
}

// hold starts the holder's server, prints its address, and serves until
// standard input closes or holderLifetime ends. It returns the exit status.
func hold(declare func(*server.Server)) int {
	srv := server.New()
	declare(srv)
	if err := srv.Start(context.Background()); err != nil {
		return reportChild("holder start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	if _, err := os.Stdout.WriteString("ADDR " + srv.State().Listeners[0].Address + "\n"); err != nil {
		return reportChild("holder address: %v", err)
	}
	stdin := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdin)
	}()
	select {
	case <-stdin:
	case <-time.After(holderLifetime):
	}
	return 0
}

// holderEnviron is this process's environment with the holder's role, every
// inherited role and depth taken out first.
func holderEnviron(role string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == holderRoleEnv || name == holderDepthEnv
	})
	return append(env, holderRoleEnv+"="+role)
}

// startHolder starts a holder of role and returns the address it bound. The
// holder is stopped — its standard input closed, then killed past
// holderWait — when the test ends, failed or not.
func startHolder(t *testing.T, role string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), holderLifetime)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$") //nolint:gosec // the test binary itself
	cmd.Env = holderEnviron(role)
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = holderWait
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("holder stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("holder stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("holder start: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("holder %s: %v", role, err)
			}
		case <-time.After(holderWait):
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("holder %s outlived its stop", role)
		}
		cancel()
	})
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			line <- sc.Text()
		}
		close(line)
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case got, ok := <-line:
		addr, found := strings.CutPrefix(got, "ADDR ")
		if !ok || !found {
			t.Fatalf("holder %s printed %q, want its address", role, got)
		}
		return addr
	case <-time.After(holderWait):
		t.Fatalf("holder %s printed no address within %v", role, holderWait)
	}
	return ""
}

// addressField is the "address" field of err, or "".
func addressField(err error) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "address" {
			return f.StringValue()
		}
	}
	return ""
}

// TestStart_addressHeldByAnotherProcess pins #1330's first criterion: a
// second process starting a server on an address a first one holds fails
// Start with AddressInUse naming the address, under the default options — a
// stream group, whose default shards bind with SO_REUSEPORT where the
// platform has it, and a datagram group given Shards(2) —, where both used to
// start and split the traffic.
func TestStart_addressHeldByAnotherProcess(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// role is the holder the other process plays.
		role string
		// declare declares this process's group on addr.
		declare func(srv *server.Server, addr string)
	}
	tests := []tc{
		{
			name: "a stream group, default options", role: "stream-default",
			declare: func(srv *server.Server, addr string) {
				srv.Group("second", server.Listen("tcp", addr)).HandleFunc(noopHandler)
			},
		},
		{
			name: "a stream group of one listener", role: "stream-default",
			declare: func(srv *server.Server, addr string) {
				srv.Group("second", server.Listen("tcp", addr), server.Shards(1)).HandleFunc(noopHandler)
			},
		},
		{
			name: "a datagram group's shards", role: "packet-shards",
			declare: func(srv *server.Server, addr string) {
				srv.PacketGroup("second", server.Listen("udp", addr), server.Shards(2)).HandleFunc(noopPacket)
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			addr := startHolder(t, c.role)
			srv := server.New()
			t.Cleanup(func() { closeOrFail(t, srv) })
			c.declare(srv, addr)
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeAddressInUse) {
				t.Fatalf("second process Start on %s = %v, want ADDRESS_IN_USE", addr, err)
			}
			if !strings.Contains(addressField(err), addr) {
				t.Fatalf("refusal names %q, want %s", addressField(err), addr)
			}
		})
	}
}

// TestShareAddress_secondProcessJoins pins the opt-in: a process that asks
// for ShareAddress starts on the address another process holds with the
// option too, State says Shared, and it serves — on Linux the kernel's hash
// spreads the connections over both processes, and every connection is
// answered by one of them. Where the platform has no SO_REUSEPORT the option
// is refused with UnsupportedPlatform, which TestShareAddress_refusals pins.
func TestShareAddress_secondProcessJoins(t *testing.T) {
	t.Parallel()
	if !shareSupported() {
		t.Skipf("no SO_REUSEPORT on %s: ShareAddress is refused there (TestShareAddress_refusals)", runtime.GOOS)
	}
	addr := startHolder(t, "stream-share")
	srv := server.New()
	t.Cleanup(func() { closeOrFail(t, srv) })
	srv.Group("second", server.Listen("tcp", addr), server.ShareAddress()).HandleFunc(answerWith(parentAnswer))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("ShareAddress Start on %s = %v, want nil", addr, err)
	}
	row := srv.State().Listeners[0]
	if !row.Shared || row.Address != addr {
		t.Fatalf("State row = %+v, want Shared on %s", row, addr)
	}
	seen := map[string]int{}
	const dials = 256
	for range dials {
		seen[dialAnswer(t, addr)]++
		if seen[parentAnswer] > 0 && (runtime.GOOS != "linux" || seen[childAnswer] > 0) {
			break
		}
	}
	if seen[parentAnswer] == 0 {
		t.Fatalf("answers %v: the process that joined served nothing", seen)
	}
	//: Linux spreads a SO_REUSEPORT group's connections by their hash; the
	//: BSDs and darwin hand them to the socket bound last.
	if runtime.GOOS == "linux" && seen[childAnswer] == 0 {
		t.Fatalf("answers %v: the holder served nothing — the address was not shared", seen)
	}
}

// shareSupported reports whether the platform has SO_REUSEPORT for the SDK.
func shareSupported() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		return true
	default:
		return false
	}
}

// dialAnswer dials addr and returns the answer line.
func dialAnswer(t *testing.T, addr string) string {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read %s: %v", addr, err)
	}
	return strings.TrimSuffix(line, "\n")
}

// TestShareAddress_refusals pins where the option cannot apply: a Unix
// socket's address, on a stream group (LoopOptionUnsupported) and on a
// datagram one (PacketOptionUnsupported), and a platform without
// SO_REUSEPORT — Windows, Solaris, illumos — with UnsupportedPlatform before
// any address is bound; where the platform has it, an IP address takes it.
func TestShareAddress_refusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declare declares the group under test.
		declare func(t *testing.T, srv *server.Server)
		// want is the refusal, zero when Start must succeed; wantElsewhere the
		// refusal on a platform without SO_REUSEPORT.
		want, wantElsewhere errs.Code
	}
	tests := []tc{
		{
			name: "a TCP address",
			declare: func(_ *testing.T, srv *server.Server) {
				srv.Group("g", server.Listen("tcp", "127.0.0.1:0"), server.ShareAddress()).HandleFunc(noopHandler)
			},
			wantElsewhere: coreproc.CodeUnsupportedPlatform,
		},
		{
			name: "a UDP address",
			declare: func(_ *testing.T, srv *server.Server) {
				srv.PacketGroup("g", server.Listen("udp", "127.0.0.1:0"), server.ShareAddress()).HandleFunc(noopPacket)
			},
			wantElsewhere: coreproc.CodeUnsupportedPlatform,
		},
		{
			name: "a Unix stream socket",
			declare: func(t *testing.T, srv *server.Server) {
				srv.Group("g", server.Listen("unix", socketDir(t)+"/s.sock"), server.ShareAddress()).HandleFunc(noopHandler)
			},
			want: corenet.CodeLoopOptionUnsupported, wantElsewhere: coreproc.CodeUnsupportedPlatform,
		},
		{
			name: "a Unix datagram socket",
			declare: func(t *testing.T, srv *server.Server) {
				srv.PacketGroup("g", server.Listen("unixgram", socketDir(t)+"/d.sock"), server.ShareAddress()).HandleFunc(noopPacket)
			},
			want: corenet.CodePacketOptionUnsupported, wantElsewhere: coreproc.CodeUnsupportedPlatform,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			t.Cleanup(func() { closeOrFail(t, srv) })
			c.declare(t, srv)
			err := srv.Start(t.Context())
			want := c.want
			if !shareSupported() {
				want = c.wantElsewhere
			}
			if want == 0 {
				if err != nil {
					t.Fatalf("Start = %v, want nil", err)
				}
				if row := srv.State().Listeners[0]; !row.Shared {
					t.Fatalf("State row = %+v, want Shared", row)
				}
				return
			}
			if !errs.HasCode(err, want) {
				t.Fatalf("Start = %v, want code %v", err, want)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("%d listeners bound beside the refusal", n)
			}
		})
	}
}
