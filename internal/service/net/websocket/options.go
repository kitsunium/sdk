// Package websocket — the connection's functional options and their defaults.
package websocket

import (
	"math"
	"slices"
	"strings"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultPingInterval is how often an idle connection proves the peer is still
// there. It sits under the idle timeout of every proxy worth naming (nginx and
// most cloud load balancers default to 60 s), so a connection that has nothing
// to say still survives the intermediaries between it and its peer.
const DefaultPingInterval time.Duration = 30 * time.Second

// DefaultWriteTimeout bounds ONE frame's write. A connection has no total write
// budget by nature; this is what stops a peer that has stopped reading from
// pinning a goroutine and a socket buffer forever.
const DefaultWriteTimeout time.Duration = 10 * time.Second

// DefaultMaxMessageSize is the ceiling on one reassembled message. It exists
// because a message's size is chosen by the PEER, one 64-bit field at a time,
// and fragmentation lets that peer keep choosing until the process dies.
const DefaultMaxMessageSize int64 = 1 << 20 // 1 MiB

// DefaultMaxFrameSize is the ceiling on a single frame's announced payload. It
// is checked before any buffer is sized, which is the whole point: the number
// being checked is the number the attacker wrote.
const DefaultMaxFrameSize int64 = 1 << 20 // 1 MiB

// Option configures a Conn.
type Option func(*config)

// config is the resolved option set. It is unexported because several fields
// have a zero value that means something specific, and the meaning is decided
// by resolve rather than by whoever fills the struct.
type config struct {
	// subprotocols is the server's preference order, most preferred first.
	subprotocols []string
	// maxMessageSize bounds one reassembled message. Zero is CLAMPED to the
	// default; negative is REFUSED. There is no spelling for "unbounded".
	maxMessageSize int64
	// maxFrameSize bounds one frame's announced payload, the same way.
	maxFrameSize int64
	// pingInterval is the heartbeat cadence. Zero is CLAMPED; negative is
	// REFUSED; "never" is noPing.
	pingInterval time.Duration
	// writeTimeout bounds one frame's write, clamped and refused the same way.
	writeTimeout time.Duration
	// noPing is the explicit spelling of "never send a ping". It is a separate
	// field rather than a sentinel duration because "never" and "use the
	// default" are different intents and one zero cannot carry both.
	noPing bool
	// allowedOrigins is the exact-match allowlist. Empty means the default
	// same-origin rule applies.
	allowedOrigins []string
	// anyOrigin disables the origin check entirely, which the caller must ask
	// for by name.
	anyOrigin bool
}

// Subprotocols declares the subprotocols this server speaks, most preferred
// first.
//
// The SERVER's order decides, not the client's: the client advertises what it
// can do, and choosing among those is the server's call — a client that listed
// a deprecated dialect first should not be able to pin the server to it.
//
// When the client offers none this server can speak, the upgrade still
// succeeds with no Sec-WebSocket-Protocol header, which RFC 6455 §4.2.2 names
// as the way to say "none agreed". Failing the handshake instead would be a
// stricter rule than the protocol has, and it would break every client that
// advertises an optional dialect.
//
// Each name must be an RFC 7230 token, which is what RFC 6455 §4.1 requires
// of a subprotocol: the chosen one is written verbatim into the response, so a
// name carrying a space, a comma or a quote would put a malformed handshake on
// the wire — it is refused at Upgrade instead. The list is copied here, so a
// caller reusing the slice it passed cannot change later handshakes, or race
// with the ones in flight.
func Subprotocols(names ...string) Option {
	snapshot := slices.Clone(names)
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.subprotocols = snapshot
	}
}

// MaxMessageSize bounds one reassembled message.
//
// Zero is clamped to [DefaultMaxMessageSize] and a negative value is refused
// (ADR 0031). There is deliberately no way to spell "unbounded": the length is
// announced by the peer in a 64-bit field, and fragmentation lets it keep
// announcing more, so an unbounded ceiling is not a configuration choice — it
// is a remote memory allocator.
func MaxMessageSize(n int64) Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.maxMessageSize = n
	}
}

// MaxFrameSize bounds one frame's ANNOUNCED payload length.
//
// It is a separate bound from [MaxMessageSize] because it is enforced at a
// different moment: the frame ceiling is checked against the header, before a
// single payload byte is read or a single byte allocated, whereas the message
// ceiling is checked as fragments accumulate. A connection that only had the
// second would have to allocate the first frame to discover it was too big.
func MaxFrameSize(n int64) Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.maxFrameSize = n
	}
}

// PingInterval sets how often the connection pings an otherwise silent peer.
//
// Zero does NOT mean "never" (ADR 0031). A connection with the heartbeat
// silently disabled works perfectly on a developer's loopback and dies at one
// minute behind a real proxy — and worse, it stops noticing a peer that has
// vanished without closing, which is the normal way a mobile client leaves.
// Zero is clamped to [DefaultPingInterval]; a negative interval is refused; to
// disable it, say so with [WithoutPing].
//
// Every interval the heartbeat sends a Ping, and one interval later it ends
// the connection if the handler has READ no frame since. It counts frames
// Receive has read, never frames that merely arrived, so it keeps a
// connection open only while one goroutine loops on Receive — see [Conn].
func PingInterval(d time.Duration) Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.pingInterval = d
		c.noPing = false
	}
}

// WithoutPing disables the heartbeat entirely.
//
// It exists so that "never" is something a caller writes on purpose rather than
// something a zero value does to them. It also disables the only liveness check
// this connection has: without it, a peer that disappears without closing —
// a laptop lid, a NAT rebinding — leaves a goroutine parked on a read that will
// never return until something else closes the socket.
func WithoutPing() Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.noPing = true
	}
}

// WriteTimeout bounds how long one frame's write may take.
//
// It is per-frame on purpose. A group's WriteTimeout is an absolute deadline
// for the whole HTTP response, which for a connection that outlives the
// response entirely would cut it at that instant; the upgrade therefore clears
// the inherited deadline and replaces it with this bound, refreshed per frame.
// Zero is clamped to [DefaultWriteTimeout], negative is refused.
func WriteTimeout(d time.Duration) Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.writeTimeout = d
	}
}

// AllowOrigins replaces the default same-origin rule with an exact allowlist.
//
// The comparison is on the whole Origin header, case-insensitively — scheme,
// host and port together. Comparing only the host would accept
// http://app.example.com for an https server, which is precisely the downgrade
// an origin check exists to notice. Behind a proxy that terminates TLS this is
// the only way to have the scheme checked at all: the request arrives in
// plaintext there, so the default rule cannot see which scheme the browser
// used (see sameOrigin).
//
// The list is copied here, for the same reason Subprotocols copies its own.
func AllowOrigins(origins ...string) Option {
	snapshot := slices.Clone(origins)
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.allowedOrigins = snapshot
		c.anyOrigin = false
	}
}

// AllowAnyOrigin disables the origin check.
//
// It has to be written out because the browser's same-origin policy does NOT
// apply to WebSocket: a page on any site can open a connection to this server
// and the browser will attach the user's cookies to the handshake. An upgrader
// that accepted every origin by default would be a cross-site request forgery
// primitive with a default-on switch, so the switch is default-off and named.
//
// Reach for it when authentication does not ride on ambient credentials — a
// bearer token in the subprotocol, a signed ticket in the query — which is
// exactly the case where the origin proves nothing anyway.
func AllowAnyOrigin() Option {
	//: applied in order by Upgrade, so a later option deliberately wins.
	return func(c *config) {
		c.anyOrigin = true
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
	//: every refusal is decided BEFORE a single default is filled in, so a
	//: rejected option set can never be handed back half-built, looking usable.
	if verr := refuseUninterpretable(&cfg); verr != nil {
		//: the error already names the offending option.
		return config{}, verr
	}
	clampToDefaults(&cfg)
	//: a frame ceiling above the message ceiling can never be reached, so one
	//: of the two numbers is a mistake and nothing here can tell which. Raising
	//: the message ceiling would loosen a bound the caller set; lowering the
	//: frame one would silently ignore a number they wrote. It is checked after
	//: the clamps because a caller who set only one of the two is comparing
	//: against a default they never wrote.
	if cfg.maxFrameSize > cfg.maxMessageSize {
		//: refuse rather than pick a side.
		return config{}, misconfigured("MaxFrameSize", cfg.maxFrameSize,
			"the frame ceiling is above the message ceiling, so it can never be reached")
	}
	//: a usable option set.
	return cfg, nil
}

// refuseUninterpretable rejects the values no SDK-chosen reading could justify.
func refuseUninterpretable(cfg *config) error {
	//: a subprotocol is echoed verbatim into the response; one that is not a
	//: token would make that response a malformed handshake.
	for index, name := range cfg.subprotocols {
		//: RFC 6455 §4.1 via RFC 7230 §3.2.6.
		if !isToken(name) {
			//: the index, since the name itself is what is malformed.
			return misconfigured("Subprotocols", int64(index),
				"a subprotocol name must be a non-empty RFC 7230 token")
		}
	}
	//: a negative interval is not a shorter one and not "never"; there is no
	//: reading of it the SDK could pick without inventing intent.
	if cfg.pingInterval < 0 {
		//: refuse rather than guess.
		return misconfigured("PingInterval", int64(cfg.pingInterval),
			"a negative interval has no meaning; use WithoutPing to disable it")
	}
	//: the same refusal for the per-frame write budget.
	if cfg.writeTimeout < 0 {
		//: refuse rather than guess.
		return misconfigured("WriteTimeout", int64(cfg.writeTimeout),
			"a negative write budget has no meaning")
	}
	//: a negative ceiling would admit every frame, which is the opposite of
	//: what the caller asked for by naming a ceiling at all.
	if cfg.maxMessageSize < 0 {
		//: refuse rather than treat it as unbounded.
		return misconfigured("MaxMessageSize", cfg.maxMessageSize,
			"a negative ceiling has no meaning, and there is no unbounded setting")
	}
	//: the same refusal for the frame ceiling.
	if cfg.maxFrameSize < 0 {
		//: refuse rather than treat it as unbounded.
		return misconfigured("MaxFrameSize", cfg.maxFrameSize,
			"a negative ceiling has no meaning, and there is no unbounded setting")
	}
	//: a ceiling past what a slice index can hold is not a bigger ceiling, it
	//: is an unrepresentable one. On a 64-bit build nothing reaches this; on a
	//: 32-bit one it is the difference between a refusal and a conversion that
	//: silently narrows the bound the caller wrote.
	if cfg.maxMessageSize > math.MaxInt {
		//: refuse rather than truncate a bound into a smaller one.
		return misconfigured("MaxMessageSize", cfg.maxMessageSize,
			"the ceiling exceeds the largest slice this platform can index")
	}
	//: nothing here is uninterpretable.
	return nil
}

// clampToDefaults fills in every unset value with the one a caller who has not
// yet learned the question exists should get.
//
// It takes a pointer rather than returning a copy because the struct is past
// the size at which passing it by value is honest, and because "fill in what is
// missing" reads better as a mutation than as a rebuild.
func clampToDefaults(cfg *config) {
	//: an unset cadence gets the working default — a heartbeat nobody thought
	//: about is still better than none.
	if cfg.pingInterval == 0 {
		cfg.pingInterval = DefaultPingInterval
	}
	//: an unset write budget gets the working default, for the same reason.
	if cfg.writeTimeout == 0 {
		cfg.writeTimeout = DefaultWriteTimeout
	}
	//: an unset ceiling gets the working default; "no ceiling" is not on offer.
	if cfg.maxMessageSize == 0 {
		cfg.maxMessageSize = DefaultMaxMessageSize
	}
	//: the same default for the frame ceiling.
	if cfg.maxFrameSize == 0 {
		cfg.maxFrameSize = DefaultMaxFrameSize
	}
	//: every field now carries a number somebody decided on.
}

// isToken reports whether s is an RFC 7230 §3.2.6 token: one or more tchar —
// ALPHA, DIGIT, or one of !#$%&'*+-.^_`|~ — and nothing else.
func isToken(s string) bool {
	//: the empty string is not a token.
	if s == "" {
		//: nothing to name.
		return false
	}
	//: every byte must be a tchar; UTF-8 has none above 0x7E.
	for i := range len(s) {
		//: one of the three classes, or the name is not a token.
		if c := s[i]; !isTokenChar(c) {
			//: a separator, a control, a space or a non-ASCII byte.
			return false
		}
	}
	//: a token.
	return true
}

// isTokenChar reports whether c is an RFC 7230 tchar.
func isTokenChar(c byte) bool {
	//: letters and digits are the common case.
	if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
		//: a tchar.
		return true
	}
	//: the fifteen punctuation characters RFC 7230 admits.
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// misconfigured builds the ADR 0031 refusal for one named option.
func misconfigured(option string, value int64, why string) error {
	//: one shape for every refusal, so a caller can branch on the option name.
	return errs.Wrap(corenet.WSConnMisconfigured, errs.WrapParams{},
		errs.String("option", option),
		errs.Int64("value", value),
		errs.String("why", why))
}
