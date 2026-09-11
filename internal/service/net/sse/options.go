// Package sse — the stream's functional options and their defaults.
package sse

import (
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultKeepAlive is the comment cadence a stream uses when the caller sets
// none. It sits under the idle timeout of every proxy worth naming — nginx and
// most cloud load balancers default to 60 s, some CDNs to 30 — so a stream that
// has nothing to say still proves it is alive before anything cuts it.
const DefaultKeepAlive time.Duration = 15 * time.Second

// DefaultWriteTimeout bounds ONE frame's write. It is not a budget for the
// stream, which by nature has none; it is what stops a peer that has stopped
// reading from pinning a goroutine and a socket buffer forever.
const DefaultWriteTimeout time.Duration = 10 * time.Second

// Option configures a Stream.
type Option func(*config)

// config is the resolved option set. It is unexported because every field has
// a zero value that means something specific, and the meaning is decided by
// resolve rather than by whoever fills the struct.
type config struct {
	// keepAlive is the comment cadence. Zero is CLAMPED to DefaultKeepAlive;
	// negative is REFUSED. See resolve.
	keepAlive time.Duration
	// writeTimeout bounds one frame's write. Zero is clamped the same way.
	writeTimeout time.Duration
	// retry, when non-zero, is sent as the stream's opening frame so the client
	// knows how long to wait before reconnecting.
	retry time.Duration
	// noKeepAlive is the explicit spelling of "never send a keep-alive". It is
	// a separate field rather than a sentinel duration because "never" and "use
	// the default" are different intents and a single zero cannot carry both.
	noKeepAlive bool
}

// KeepAlive sets the interval between keep-alive comments.
//
// Zero does NOT mean "never" (ADR 0031): a stream with keep-alive silently
// disabled works perfectly on a developer's loopback and dies at one minute
// behind a real proxy, which is the worst possible place to learn it. Zero is
// clamped to DefaultKeepAlive; a negative interval is refused, because no value
// the SDK invented for it would be defensible. To disable it, say so with
// [WithoutKeepAlive].
func KeepAlive(d time.Duration) Option {
	//: applied in order by New, so a later option deliberately wins.
	return func(c *config) {
		c.keepAlive = d
		c.noKeepAlive = false
	}
}

// WithoutKeepAlive disables the keep-alive comment entirely.
//
// It exists so that "never" is something a caller writes on purpose rather than
// something a zero value does to them. Reach for it when the stream already
// carries traffic more often than any intermediary's idle timeout, or when
// there is no intermediary at all.
func WithoutKeepAlive() Option {
	//: applied in order by New, so a later option deliberately wins.
	return func(c *config) {
		c.noKeepAlive = true
	}
}

// WriteTimeout bounds how long one frame's write may take.
//
// It is deliberately per-frame. A group's WriteTimeout is an absolute deadline
// for the whole response, which for an endless stream means the stream is cut
// at that instant; the stream therefore replaces it with this bound, refreshed
// on every frame. Zero is clamped to DefaultWriteTimeout, negative is refused.
func WriteTimeout(d time.Duration) Option {
	//: applied in order by New, so a later option deliberately wins.
	return func(c *config) {
		c.writeTimeout = d
	}
}

// Retry sets the reconnection delay the stream advertises in its opening frame.
//
// It is the server's one chance to control how hard clients come back: a fleet
// that all reconnect on the browser default of about three seconds is a
// thundering herd aimed at a server that has just restarted. Zero sends no
// retry field and leaves the client on its own default.
func Retry(d time.Duration) Option {
	//: applied in order by New, so a later option deliberately wins.
	return func(c *config) {
		c.retry = d
	}
}

// resolve turns the raw option set into a usable one: it clamps where a working
// default needs no explanation, and refuses where any value the SDK chose would
// be arbitrary (ADR 0031).
func resolve(opts []Option) (resolved config, err error) {
	var cfg config
	//: options apply in order, so a later one deliberately wins.
	for _, opt := range opts {
		opt(&cfg)
	}
	//: a negative interval is not a shorter one and not "never"; there is no
	//: reading of it the SDK could pick without inventing intent.
	if cfg.keepAlive < 0 {
		//: refuse rather than guess.
		return config{}, errs.Wrap(corenet.SSEStreamMisconfigured, errs.WrapParams{},
			errs.String("option", "KeepAlive"),
			errs.Int64("nanoseconds", int64(cfg.keepAlive)),
			errs.String("why", "a negative interval has no meaning; use WithoutKeepAlive to disable it"))
	}
	//: the same refusal for the per-frame write budget.
	if cfg.writeTimeout < 0 {
		//: refuse rather than guess.
		return config{}, errs.Wrap(corenet.SSEStreamMisconfigured, errs.WrapParams{},
			errs.String("option", "WriteTimeout"),
			errs.Int64("nanoseconds", int64(cfg.writeTimeout)),
			errs.String("why", "a negative write budget has no meaning"))
	}
	//: an unset cadence gets the working default — a keep-alive nobody thought
	//: about is still better than none.
	if cfg.keepAlive == 0 {
		cfg.keepAlive = DefaultKeepAlive
	}
	//: an unset write budget gets the working default, for the same reason.
	if cfg.writeTimeout == 0 {
		cfg.writeTimeout = DefaultWriteTimeout
	}
	//: the retry hint is validated by the frame that carries it, so a bad one
	//: is reported with the same code whether it came from here or from Send.
	if verr := (corenet.SSEEventValue{Retry: corenet.DurationValue(cfg.retry)}).Validate(); cfg.retry != 0 && verr != nil {
		//: surface the field-level refusal unchanged.
		return config{}, verr
	}
	//: a usable option set.
	return cfg, nil
}
