// Package redis — the client seam: the ONLY file that imports the Redis driver.
// newClient builds a lazy client over a Unix socket and returns the execBatch
// closure (a pipelined XADD run) plus a closeFn that releases the client.
// Returning a closeFn rather than the *redis.Client keeps the driver type out of
// the sink wrapper, so go-redis stays confined to this file.
//
// Package redis — range 0.3.34.* (ADR 0015 service slot 0x22).
//
// Package redis — declares the sentinels returned by the Redis writer's
// constructor and XADD path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form (short names per the AWS-writer convention; the package
// qualifier gives context). No socket path, credential, or record value is ever
// echoed into these errors (secret gate).
//
// Package redis registers the "redis" writer factory (ADR 0015): a log sink that
// appends records to a Redis Stream via pipelined XADD over a Unix-domain socket
// (local-protocol). Importing the package self-registers the factory (no
// init()), so writer.Open("redis", writer.RedisStreamConfig{…}) resolves. It is a
// dep-light third-party integration: the github.com/redis/go-redis/v9 import is
// confined to client.go, in a module of its own (ADR 0157), so pkg/v1 consumers
// never pull the driver into their graph and a consumer of this writer pulls no
// other vendor.
//
// Credentials: RedisStreamConfig.Credentials is OPTIONAL (a socket-local Redis
// may have no AUTH) and, when set, supplied programmatically — like the AWS
// writers, there is no config-file Decoder because a live credential cannot be
// expressed safely in YAML.
//
// Package redis — redisSink, the thin wrapper that adds client cleanup to the
// dbsink-composed chain. It holds a closeFn (not the *redis.Client) so the
// driver type stays confined to client.go; Close drains the chain then releases
// the client exactly once.
package redis
