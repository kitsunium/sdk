//go:build linux

// Package loop — the drain (plan P6): the listen queue taken before the
// listener closes, and the connections it held served, on every backend.
package loop

import (
	"bufio"
	"context"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestConformance_drainOutOfDescriptors drains a child serving under
// RLIMIT_NOFILE=64 with drainShort connections dialled, most of them still
// in the listen queue — on epoll the accept is out of descriptors, on
// io_uring the direct table is full. The drain's take must wait for the
// closes it makes, batch after batch, not hand the queue to the close: every
// connection reads its echo, then a clean end, and Shutdown returns nil.
func TestConformance_drainOutOfDescriptors(t *testing.T) {
	const drainShort = 150
	eachBackend(t, func(t *testing.T, b backendCase) {
		child := startDrainChild(t, b, 64)
		conns := make([]stdnet.Conn, 0, drainShort)
		for range drainShort {
			c, err := stdnet.DialTimeout("tcp", child.addr, 3*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			t.Cleanup(func() { _ = c.Close() })
			_, _ = c.Write([]byte("q\n"))
			conns = append(conns, c)
		}
		if _, err := io.WriteString(child.stdin, "drain\n"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		failures := make(chan string, drainShort)
		for i, c := range conns {
			wg.Go(func() {
				if msg := echoThenEnd(c, "q\n"); msg != "" {
					failures <- msg + " (#" + strconv.Itoa(i) + ")"
				}
			})
		}
		wg.Wait()
		close(failures)
		var all []string
		for f := range failures {
			all = append(all, f)
		}
		report := child.stop(t)
		if len(all) > 0 {
			t.Fatalf("%d of %d cut short: %v; child: %s", len(all), drainShort, all[:min(len(all), 8)], report)
		}
		if !strings.Contains(report, "shutdown=<nil>") {
			t.Fatalf("the drain did not end on its own: %s", report)
		}
	})
}

// startDrainChild starts a child serving on b under RLIMIT_NOFILE=rlimit,
// which drains at the line "drain" on its standard input.
func startDrainChild(t *testing.T, b backendCase, rlimit int) *serveChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=drain:%s:%d", childEnv, b.name, rlimit))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	addr, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	return &serveChild{cmd: cmd, stdin: stdin, out: r, addr: strings.TrimSpace(addr)}
}

// childDrain is the drain child: "<backend>:<rlimit>". It serves, drains
// at the line "drain", prints Shutdown's result and returns once stdin closes.
func childDrain(spec string) int {
	name, lim, _ := strings.Cut(spec, ":")
	var b corenet.LoopBackend
	for _, x := range backendsPresent() {
		if x.name == name {
			b = x.backend
		}
	}
	rlimit, _ := strconv.Atoi(lim)
	// The budget's timer first: the runtime's netpoller needs a descriptor of
	// its own, which the loop would otherwise have taken by the drain.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: uint64(rlimit), Max: uint64(rlimit)}); err != nil {
		fmt.Println("setrlimit:", err)
		return 1
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 1
	}
	_ = syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
	_ = syscall.Listen(fd, 1024)
	sa, _ := syscall.Getsockname(fd)
	g, err := Start(Config{Group: "child", Value: corenet.LoopValue{Backend: b}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
	if err != nil {
		fmt.Println("start:", err)
		return 1
	}
	fmt.Printf("127.0.0.1:%d\n", sa.(*syscall.SockaddrInet4).Port)
	in := bufio.NewReader(os.Stdin)
	_, _ = in.ReadString('\n')
	err = g.Shutdown(ctx)
	_, _ = io.Copy(io.Discard, in)
	fmt.Printf("shutdown=%v\n", err)
	_ = g.Close()
	return 0
}
