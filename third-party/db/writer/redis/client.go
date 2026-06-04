// Package redis — the client seam: the ONLY file that imports the Redis driver.
// newClient builds a lazy client over a Unix socket and returns the execBatch
// closure (a pipelined XADD run) plus a closeFn that releases the client.
// Returning a closeFn rather than the *redis.Client keeps the driver type out of
// the sink wrapper, so go-redis stays confined to this file.
package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
)

// newClient validates the config, optionally resolves credentials, and builds a
// lazy Redis client (no connection until the first command). It returns the
// deliver closure + a closeFn for the sink wrapper to call on Close.
func newClient(c writer.RedisStreamConfig) (exec func(context.Context, []corelogger.RecordEvent) error, closeFn func() error, err error) {
	//: a socket and a stream key are both mandatory.
	if c.SocketPath == "" || c.Stream == "" {
		//: surface the client-init sentinel (no value to echo).
		return nil, nil, ClientInitFailed
	}
	//: resolve optional AUTH once (empty user/pass when no provider).
	user, pass, cerr := resolveCreds(c.Credentials)
	//: forward a credential-resolution failure under the init sentinel.
	if cerr != nil {
		//: wrapClientInit never echoes the credentials.
		return nil, nil, wrapClientInit(cerr)
	}
	//: local-protocol: dial the Unix domain socket, never TCP.
	rdb := goredis.NewClient(&goredis.Options{
		Network: "unix", Addr: c.SocketPath, Username: user, Password: pass,
	})
	stream, maxLen := c.Stream, c.MaxLen
	//: the deliver closure pipelines one XADD per record; the DB-touching Exec
	//: lives here (anonymous) so the package exposes no untestable named method.
	exec = func(ctx context.Context, batch []corelogger.RecordEvent) error {
		//: an empty batch is a no-op (guard; the batcher never delivers one).
		if len(batch) == 0 {
			//: nothing to append.
			return nil
		}
		pipe := rdb.Pipeline()
		//: queue one XADD per record into the pipeline.
		for _, r := range batch {
			//: cap the stream length with MAXLEN ~ when configured.
			args := &goredis.XAddArgs{Stream: stream, Values: buildValues(r)}
			//: a positive MaxLen enables approximate trimming (whole macro-nodes).
			if maxLen > 0 {
				//: approximate trim is cheaper than exact and standard for logs.
				args.MaxLen = int64(maxLen)
				args.Approx = true
			}
			pipe.XAdd(ctx, args)
		}
		//: execute the whole pipeline in one round-trip.
		if _, eerr := pipe.Exec(ctx); eerr != nil {
			//: wrapAdd never echoes the payload.
			return wrapAdd(eerr, len(batch))
		}
		//: the batch landed.
		return nil
	}
	//: hand back the deliver seam + the client's Close as the closeFn.
	return exec, rdb.Close, nil
}

// resolveCreds reads the optional credential provider, returning empty
// user/password when none is supplied (a socket-local Redis may have no AUTH).
// Split from newClient so the constructor stays within the size + complexity cap.
func resolveCreds(cp writer.CredentialProvider) (user, password string, err error) {
	//: no provider means no AUTH — empty user/password.
	if cp == nil {
		//: socket-local Redis without a password.
		return "", "", nil
	}
	//: resolve the login once; the DSN/client is static for the pool.
	cred, cerr := cp.Credentials(context.Background())
	//: a provider failure aborts construction.
	if cerr != nil {
		//: surface the cause for newClient to wrap.
		return "", "", cerr
	}
	//: AccessKeyID() is the ACL username, SecretAccessKey() the password.
	return cred.AccessKeyID(), cred.SecretAccessKey(), nil
}

// buildValues maps a record to the XADD field set. Pure (no driver) so it is
// unit-tested directly; the timestamp is nanoseconds for lossless ordering.
func buildValues(r corelogger.RecordEvent) map[string]any {
	//: three fields mirror the SQL schema: ts, level, message.
	return map[string]any{
		"ts":      r.Time.UnixNano(),
		"level":   r.Level.String(),
		"message": r.Message,
	}
}
