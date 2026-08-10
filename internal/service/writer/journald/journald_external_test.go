package journald_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/writer/journald"
)

// readDeadline bounds the server-side datagram read so a lost send fails loud
// instead of hanging the suite.
const readDeadline time.Duration = 2 * time.Second

// sunPathMax bounds a unix socket path: sockaddr_un.sun_path is a fixed-size
// field, and a path that fills it makes bind(2) fail with EINVAL.
const sunPathMax int = 104

// shortSocketPath returns a bindable unix socket path under a temp directory.
// t.TempDir() embeds the full subtest name, which on macOS pushes the path
// under /var/folders past sunPathMax and breaks the bind before the test runs.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	//: "jd" keeps the generated component down to a handful of bytes.
	dir, err := os.MkdirTemp("", "jd")
	if err != nil {
		//: without a directory there is no socket to bind; fail loud.
		t.Fatalf("MkdirTemp: %v", err)
	}
	//: t.TempDir() would have removed itself; match that.
	t.Cleanup(func() {
		//: cleanup runs past assertion time, so a stuck directory is reported
		//: rather than allowed to mask the real result.
		if rerr := os.RemoveAll(dir); rerr != nil {
			t.Logf("RemoveAll %s: %v", dir, rerr)
		}
	})

	path := filepath.Join(dir, "j.sock")
	//: report the length rather than let bind(2) fail with an opaque EINVAL.
	if len(path) >= sunPathMax {
		t.Fatalf("socket path is %d bytes, over the sun_path limit: %s", len(path), path)
	}

	return path
}

// closeIgnore drops a fixture Close error in the black-box tests: cleanup runs
// past assertion time, so a close failure must not mask the real result.
func closeIgnore(err error) {
	//: read the parameter so the discard is explicit, not a bare `_ =`.
	if err == nil {
		//: nothing to drop on the happy path.
		return
	}
}

func Test_journaldRegistered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  writer.Name
	}{
		{"journald is registered", "journald"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: importing the package self-registers the journald factory.
			if !tc.key.Known() {
				t.Errorf("%s: writer key %q not registered", tc.name, tc.key)
			}
		})
	}
}

// Test_journaldSink_UnixgramRoundtrip drives the production Write end-to-end over
// a REAL bound unixgram socket: writer.Open with a nil Dialer exercises the
// default net.Dial branch, the composed async chain ships the datagram, and the
// in-process server reads back the framed "MESSAGE=<payload>" entry. This is the
// realistic I/O E2E — the actual unixgram codepath, not the net.Pipe stream the
// white-box tests use.
func Test_journaldSink_UnixgramRoundtrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
	}{
		{"datagram arrives framed as a journal MESSAGE", "hello-journald"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: bind a real datagram socket so the default dialer has a live peer.
			path := shortSocketPath(t)
			srv, lerr := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
			//: a private-tempdir unixgram bind is reliable; a failure is a hard
			//: fault that must fail the suite, never silently skip the E2E.
			if lerr != nil {
				t.Fatalf("%s: ListenUnixgram: %v", tc.name, lerr)
			}
			t.Cleanup(func() { closeIgnore(srv.Close()) })

			//: nil Dialer routes through net.Dial — the real connect path.
			sink, oerr := writer.Open("journald", journald.Config{SocketPath: path})
			//: the registered factory must build a live sink over the socket.
			if oerr != nil || sink == nil {
				t.Fatalf("%s: writer.Open: sink=%v err=%v", tc.name, sink, oerr)
			}
			t.Cleanup(func() { closeIgnore(sink.Close()) })

			//: write through the production chain, then Flush so the async ring
			//: drains the datagram to the socket before the server reads.
			if _, werr := sink.Write(t.Context(), corelogger.RecordEvent{}, []byte(tc.payload)); werr != nil {
				t.Fatalf("%s: Write: %v", tc.name, werr)
			}
			if ferr := sink.Flush(t.Context()); ferr != nil {
				t.Fatalf("%s: Flush: %v", tc.name, ferr)
			}

			//: a deadline turns a lost datagram into a failure, never a hang.
			if derr := srv.SetReadDeadline(time.Now().Add(readDeadline)); derr != nil {
				t.Fatalf("%s: SetReadDeadline: %v", tc.name, derr)
			}
			buf := make([]byte, 128)
			n, rerr := srv.Read(buf)
			//: the server must observe exactly the framed entry the sink sent.
			if rerr != nil {
				t.Fatalf("%s: server Read: %v", tc.name, rerr)
			}
			//: the framed datagram is "MESSAGE=<payload>\n" — assert both halves.
			if frame := string(buf[:n]); !strings.HasPrefix(frame, "MESSAGE=") || !strings.Contains(frame, tc.payload) {
				t.Errorf("%s: frame=%q want MESSAGE=…%s", tc.name, frame, tc.payload)
			}
		})
	}
}
