// Package websocket — white-box tests for the option resolution rules.
//
// They live inside the package because neither half of ADR 0031 is observable
// from outside it: a clamped default only shows up thirty seconds later, and a
// refusal is indistinguishable at the edge from any other misconfiguration.
package websocket

import (
	"math"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_resolveClampsWhereADefaultNeedsNoExplanation pins the ADR 0031 clamp.
//
// The zero values matter more than they look: a heartbeat silently disabled
// works perfectly on a developer's loopback and stops noticing vanished peers
// in production, and an unset ceiling would be a remote memory allocator.
func Test_resolveClampsWhereADefaultNeedsNoExplanation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		opts []Option
		want config
	}
	tests := []tc{
		{
			name: "nothing set at all",
			want: config{
				pingInterval:   DefaultPingInterval,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
		{
			name: "an explicit zero is not 'never'",
			opts: []Option{PingInterval(0), WriteTimeout(0), MaxMessageSize(0), MaxFrameSize(0)},
			want: config{
				pingInterval:   DefaultPingInterval,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
		{
			name: "'never' has its own spelling",
			opts: []Option{WithoutPing()},
			want: config{
				noPing:         true,
				pingInterval:   DefaultPingInterval,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
		{
			name: "a later PingInterval undoes an earlier WithoutPing",
			opts: []Option{WithoutPing(), PingInterval(time.Second)},
			want: config{
				pingInterval:   time.Second,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
		{
			name: "a later WithoutPing undoes an earlier PingInterval",
			opts: []Option{PingInterval(time.Second), WithoutPing()},
			want: config{
				noPing:         true,
				pingInterval:   time.Second,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
		{
			name: "a later AllowOrigins undoes AllowAnyOrigin",
			opts: []Option{AllowAnyOrigin(), AllowOrigins("https://app.example")},
			want: config{
				allowedOrigins: []string{"https://app.example"},
				pingInterval:   DefaultPingInterval,
				writeTimeout:   DefaultWriteTimeout,
				maxMessageSize: DefaultMaxMessageSize,
				maxFrameSize:   DefaultMaxFrameSize,
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolve(c.opts)
			if err != nil {
				t.Fatalf("resolve = %v, want it accepted", err)
			}
			if got.pingInterval != c.want.pingInterval ||
				got.writeTimeout != c.want.writeTimeout ||
				got.maxMessageSize != c.want.maxMessageSize ||
				got.maxFrameSize != c.want.maxFrameSize ||
				got.noPing != c.want.noPing ||
				got.anyOrigin != c.want.anyOrigin ||
				len(got.allowedOrigins) != len(c.want.allowedOrigins) {
				t.Fatalf("resolved = %+v\nwant       %+v", got, c.want)
			}
		})
	}
}

// Test_resolveRefusesWhereAnySDKChoiceWouldBeArbitrary pins the other half of
// ADR 0031: where no value the SDK invented would be defensible, it refuses.
func Test_resolveRefusesWhereAnySDKChoiceWouldBeArbitrary(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		opts   []Option
		option string
	}
	tests := []tc{
		{"a negative ping interval", []Option{PingInterval(-time.Second)}, "PingInterval"},
		{"a negative write budget", []Option{WriteTimeout(-time.Nanosecond)}, "WriteTimeout"},
		{"a negative message ceiling", []Option{MaxMessageSize(-1)}, "MaxMessageSize"},
		{"a negative frame ceiling", []Option{MaxFrameSize(-1)}, "MaxFrameSize"},
		{
			"a frame ceiling that can never be reached",
			[]Option{MaxMessageSize(100), MaxFrameSize(200)},
			"MaxFrameSize",
		},
		//: a subprotocol is echoed verbatim into the response, so each of
		//: these would have put a malformed handshake on the wire — they
		//: were accepted before the token check.
		{"an empty subprotocol", []Option{Subprotocols("chat", "")}, "Subprotocols"},
		{"a subprotocol with a space", []Option{Subprotocols("chat v2")}, "Subprotocols"},
		{"a subprotocol with a comma", []Option{Subprotocols("a,b")}, "Subprotocols"},
		{"a subprotocol with a line break", []Option{Subprotocols("chat\r\nX-Injected: 1")}, "Subprotocols"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolve(c.opts)
			if err == nil {
				t.Fatalf("resolve = %+v, want a refusal", got)
			}
			if !errs.HasCode(err, corenet.CodeWSConnMisconfigured) {
				t.Fatalf("resolve error = %v, want WS_CONN_MISCONFIGURED", err)
			}
			//: the refusal must name the option, or a caller with six of them
			//: set has to bisect to find the one that was wrong.
			named := false
			for _, field := range errs.FieldsOf(err) {
				if field.Key() == "option" {
					named = true
				}
			}
			if !named {
				t.Fatalf("the refusal does not name an option: %v", err)
			}
			//: a refusal must not hand back a half-built configuration a caller
			//: could mistake for a usable one.
			if got.pingInterval != 0 || got.writeTimeout != 0 ||
				got.maxMessageSize != 0 || got.maxFrameSize != 0 {
				t.Fatalf("resolve returned %+v alongside its refusal, want the zero config", got)
			}
		})
	}
}

// Test_resolveBoundsACeilingByWhatASliceCanIndex covers the one guard whose
// answer depends on the word size of the build.
//
// It is written as an if rather than skipped on 64-bit because the assertion is
// different, not absent: where int is as wide as int64 the ceiling IS
// representable and must be accepted, and where it is not the value would be
// truncated into a much smaller bound than the caller wrote — which is exactly
// the silent narrowing ADR 0031 refuses. ADR 0018's build bar compiles both.
func Test_resolveBoundsACeilingByWhatASliceCanIndex(t *testing.T) {
	t.Parallel()
	got, err := resolve([]Option{MaxMessageSize(math.MaxInt64), MaxFrameSize(math.MaxInt64)})
	//: a 64-bit build can index it, so there is nothing to refuse.
	if math.MaxInt == math.MaxInt64 {
		if err != nil {
			t.Fatalf("resolve = %v, want a representable ceiling accepted", err)
		}
		if got.maxMessageSize != math.MaxInt64 {
			t.Fatalf("maxMessageSize = %d, want it kept as written", got.maxMessageSize)
		}
		return
	}
	//: a 32-bit build cannot, and truncating would be the silent narrowing.
	if !errs.HasCode(err, corenet.CodeWSConnMisconfigured) {
		t.Fatalf("resolve = %v, want WS_CONN_MISCONFIGURED", err)
	}
}

// Test_optionsSnapshotTheirSlices pins that Subprotocols and AllowOrigins copy
// the slice they are handed. Captured by reference, a caller reusing its slice
// changed every later handshake and raced with the ones in flight — seen
// failing so, with the copies removed: "subprotocols = [edited], want [chat]".
func Test_optionsSnapshotTheirSlices(t *testing.T) {
	t.Parallel()
	protocols := []string{"chat"}
	origins := []string{"https://app.example.com"}
	opts := []Option{Subprotocols(protocols...), AllowOrigins(origins...)}
	protocols[0], origins[0] = "edited", "https://evil.example.com"
	got, err := resolve(opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got.subprotocols) != 1 || got.subprotocols[0] != "chat" {
		t.Errorf("subprotocols = %v, want [chat]", got.subprotocols)
	}
	if len(got.allowedOrigins) != 1 || got.allowedOrigins[0] != "https://app.example.com" {
		t.Errorf("allowedOrigins = %v, want [https://app.example.com]", got.allowedOrigins)
	}
}

// Test_isToken pins the RFC 7230 §3.2.6 grammar the subprotocol check applies.
func Test_isToken(t *testing.T) {
	t.Parallel()
	type tc struct {
		in   string
		want bool
	}
	tests := []tc{
		{"chat", true},
		{"v2.json-rpc_1~x", true},
		{"!#$%&'*+-.^_`|~", true},
		{"", false},
		{"a b", false},
		{"a,b", false},
		{`"quoted"`, false},
		{"a/b", false},
		{"é", false},
		{"a\tb", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isToken(c.in); got != c.want {
			t.Errorf("isToken(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
