//go:build integration

// Requires: go get github.com/testcontainers/testcontainers-go github.com/testcontainers/testcontainers-go/modules/redis

package redis_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	rds "github.com/kitsunium/sdk/third-party/db/writer/redis"
)

// containerSockDir is the in-container directory the Unix socket is created in;
// it is bind-mounted from a host temp dir so the production factory (which dials
// net=unix) can reach the real socket.
const containerSockDir = "/sock"

// readySocketTimeout bounds how long we wait for redis to create its socket
// after the container reports "Ready to accept connections".
const readySocketTimeout = 5 * time.Second

// startRedis boots a redis:7-alpine container that listens on BOTH a Unix socket
// (bind-mounted to the host for the production factory) and TCP (for the raw
// read-back client). It returns the host-side socket path plus a connected raw
// client for verification. On any docker failure it skips so a Docker-less CI
// stays green.
func startRedis(ctx context.Context, t *testing.T) (sockPath string, raw *goredis.Client) {
	t.Helper()
	//: a per-test host dir holds the socket; 0o777 so the in-container redis
	//: user can create the socket file under the bind mount.
	hostDir := t.TempDir()
	if cerr := os.Chmod(hostDir, 0o777); cerr != nil {
		t.Skipf("chmod host socket dir: %v", cerr)
	}
	ctr, err := tcredis.Run(
		ctx, "redis:7-alpine",
		//: enable the Unix socket in addition to the default TCP port.
		testcontainers.WithCmd(
			"redis-server",
			"--unixsocket", containerSockDir+"/redis.sock",
			"--unixsocketperm", "777",
		),
		//: bind-mount the host dir so the created socket is reachable off-box.
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, hostDir+":"+containerSockDir)
		}),
	)
	//: docker unavailable (no daemon / no socket) — skip, never fail CI.
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	//: tear the container down regardless of assertion outcome.
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	opt, err := goredis.ParseURL(dsn)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	raw = goredis.NewClient(opt)
	//: close the read-back client with the container.
	t.Cleanup(func() { _ = raw.Close() })

	sockPath = hostDir + "/redis.sock"
	//: the socket appears a beat after the TCP-port readiness log — poll for it.
	waitForSocket(t, sockPath)
	return sockPath, raw
}

// waitForSocket blocks until the host-side socket file exists or the deadline
// elapses, so the production factory's first XADD does not race socket creation.
func waitForSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(readySocketTimeout)
	for time.Now().Before(deadline) {
		//: the socket file's presence is the local-protocol readiness signal.
		if _, err := os.Stat(sockPath); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("redis unix socket never appeared at %s", sockPath)
}

func Test_Integration_RedisWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sockPath, raw := startRedis(ctx, t)

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			//: full round-trip: three records survive into the stream verbatim.
			name: "writes a batch and reads back from the stream",
			run: func(t *testing.T) {
				stream := "test-logs-roundtrip"
				sink, err := rds.Writer.Open(writer.RedisStreamConfig{
					SocketPath: sockPath, Stream: stream, MaxRows: 10,
				})
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				recs := []corelogger.RecordEvent{
					{Time: time.Unix(0, 100), Level: level.Info, Message: "first"},
					{Time: time.Unix(0, 200), Level: level.Warn, Message: "second"},
					{Time: time.Unix(0, 300), Level: level.Error, Message: "third"},
				}
				writeAll(t, sink, recs)
				flushClose(t, sink)

				entries, xerr := raw.XRange(ctx, stream, "-", "+").Result()
				if xerr != nil {
					t.Fatalf("XRANGE: %v", xerr)
				}
				//: exactly the three appended records land, in order.
				if len(entries) != len(recs) {
					t.Fatalf("entries=%d want %d", len(entries), len(recs))
				}
				for i, e := range entries {
					assertEntry(t, e.Values, recs[i])
				}
			},
		},
		{
			//: MaxLen bounds the stream via approximate (macro-node) trimming.
			name: "MaxLen trims the stream",
			run: func(t *testing.T) {
				stream := "test-logs-trim"
				sink, err := rds.Writer.Open(writer.RedisStreamConfig{
					SocketPath: sockPath, Stream: stream, MaxLen: 2, MaxRows: 10,
				})
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				recs := make([]corelogger.RecordEvent, 10)
				for i := range recs {
					recs[i] = corelogger.RecordEvent{Time: time.Unix(0, int64(i+1)), Level: level.Info, Message: "trim"}
				}
				writeAll(t, sink, recs)
				flushClose(t, sink)

				n, xerr := raw.XLen(ctx, stream).Result()
				if xerr != nil {
					t.Fatalf("XLEN: %v", xerr)
				}
				//: approximate trim may leave a few extra entries; assert the
				//: bound is enforced without demanding an exact length.
				if n > 4 {
					t.Errorf("XLEN=%d want <=4 (MaxLen=2 approx)", n)
				}
			},
		},
		{
			//: an unreachable endpoint surfaces AddFailed via the OnError hook.
			name: "AddFailed error is surfaced via OnError callback",
			run: func(t *testing.T) {
				var (
					mu  sync.Mutex
					got error
				)
				sink, err := rds.Writer.Open(writer.RedisStreamConfig{
					//: a socket that will never exist forces the XADD to fail.
					SocketPath: t.TempDir() + "/dead.sock",
					Stream:     "test-logs-dead",
					MaxRows:    1,
					OnError: func(e error) {
						mu.Lock()
						got = e
						mu.Unlock()
					},
				})
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				writeAll(t, sink, []corelogger.RecordEvent{
					{Time: time.Unix(0, 1), Level: level.Error, Message: "doomed"},
				})
				//: Flush forces the pipelined XADD; Close drains the drainer.
				_ = sink.Flush(ctx)
				_ = sink.Close()

				mu.Lock()
				defer mu.Unlock()
				//: the drainer must have reported the documented add sentinel.
				if !errs.HasCode(got, rds.CodeRedisXAddFailed) {
					t.Errorf("OnError got=%v want code %v", got, rds.CodeRedisXAddFailed)
				}
			},
		},
		{
			//: the public registry path resolves the factory and builds a sink.
			name: "registered factory resolves and builds a real sink",
			run: func(t *testing.T) {
				sink, err := writer.Open("redis", writer.RedisStreamConfig{
					SocketPath: sockPath, Stream: "test-logs-registry", MaxRows: 10,
				})
				if err != nil || sink == nil {
					t.Fatalf("writer.Open: sink=%v err=%v", sink, err)
				}
				//: a freshly built sink closes cleanly over the live socket.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("Close: %v", cerr)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

// writeAll pushes every record through the sink, failing on the first Write
// error so a broken transport is caught at the source.
func writeAll(t *testing.T, sink corelogger.Sink, recs []corelogger.RecordEvent) {
	t.Helper()
	for i, r := range recs {
		//: the payload bytes are opaque to the redis sink (it reads the record).
		if _, err := sink.Write(context.Background(), r, []byte(r.Message)); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}
}

// flushClose forces the pending batch out then drains, failing on either error.
func flushClose(t *testing.T, sink corelogger.Sink) {
	t.Helper()
	//: Flush forces the pipelined XADD before the read-back.
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	//: Close drains the async drainer and releases the client.
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// assertEntry checks that a stream entry's fields mirror the source record.
func assertEntry(t *testing.T, vals map[string]any, want corelogger.RecordEvent) {
	t.Helper()
	//: ts round-trips as the decimal UnixNano string redis stores.
	if got := vals["ts"]; got != stamp(want.Time) {
		t.Errorf("ts=%v want %v", got, stamp(want.Time))
	}
	//: level round-trips as its human label.
	if got := vals["level"]; got != want.Level.String() {
		t.Errorf("level=%v want %v", got, want.Level.String())
	}
	//: message survives verbatim.
	if got := vals["message"]; got != want.Message {
		t.Errorf("message=%v want %v", got, want.Message)
	}
}

// stamp renders a record time the way redis returns the stored int64 field: a
// decimal string of the nanosecond stamp (XADD coerces the int64 to text).
func stamp(ts time.Time) string {
	//: redis stores every field as a string, so the int64 comes back decimal.
	return strconv.FormatInt(ts.UnixNano(), 10)
}
