// Package kit — the HTTP request and response an endpoint can reach.
package kit

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// maxUserAgent bounds the user agent a handler reads: a session list shows
// it, and a header can be as long as a client likes.
const maxUserAgent int = 256

// discardLogger is the logger of code running outside any app: it writes
// nothing.
var discardLogger = sync.OnceValue(func() logger.Logger {
	sink, err := logger.NewWriterSink(io.Discard)
	if err != nil {
		panic("kit: the SDK refused a discarding sink: " + err.Error())
	}
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: logger.NewJSONEncoder(), MinLevel: logger.LevelError})
	if err != nil {
		panic("kit: the SDK refused a discarding logger: " + err.Error())
	}
	return lg
})

// responseState is what a handler may add to the HTTP response it runs for,
// and what it may read about the request's client. The endpoint's HTTP path
// puts one in the handler's context; an in-process call has none.
type responseState struct {
	// mu guards cookies and sealed: a policy such as Timeout may return
	// while the handler still runs, so a late SetCookie races the response.
	mu      sync.Mutex
	cookies []*http.Cookie
	// sealed is set once the response is written: later cookies are dropped.
	sealed    bool
	clientIP  string
	userAgent string
}

type responseKey struct{}

// The request-scoped helpers a handler uses: cookies, the client's address
// and user agent, the app's time and logger.

// SetCookie adds a Set-Cookie header to the HTTP response of the endpoint
// ctx serves. kit fills what a cookie left zero with safe defaults — Path
// "/", HttpOnly, SameSite Lax, and Secure outside dev — so a session cookie
// is hardened unless the code says otherwise. In an in-process
// [EndpointService.Call] there is no response: the cookie is dropped.
//
// HttpOnly is always set, and Secure always outside dev (and in dev for
// SameSite=None, which browsers require): a boolean has no "unset" a default
// could fill, and kit chooses the hardened reading. A cookie set more than
// once in one response keeps its last value, and cookies set before an error
// are sent with the error — clearing a stale session on a 401 works.
func SetCookie(ctx context.Context, c *http.Cookie) {
	if rs := responseStateOf(ctx); rs != nil && c != nil {
		rs.add(c)
	}
}

// ClearCookie tells the browser to forget the cookie called name.
func ClearCookie(ctx context.Context, name string) {
	SetCookie(ctx, &http.Cookie{Name: name, Value: "", MaxAge: -1})
}

// ClientIP returns the address of the client of the request ctx serves: the
// connection's peer, or — only when KIT_TRUST_PROXY=on — the last address of
// X-Forwarded-For, the one the trusted proxy appended. It is "" outside an HTTP request.
func ClientIP(ctx context.Context) string {
	if rs := responseStateOf(ctx); rs != nil {
		return rs.clientIP
	}
	return ""
}

// UserAgent returns the User-Agent of the request ctx serves, clipped.
func UserAgent(ctx context.Context) string {
	if rs := responseStateOf(ctx); rs != nil {
		return rs.userAgent
	}
	return ""
}

// Now returns the app's time: the system clock, or the clock a test gave
// with [Clock]. Product code that compares with a deadline reads it rather
// than time.Now, so that a test's manual clock reaches it.
func Now(ctx context.Context) time.Time {
	return appClock(ctx).Now()
}

// NewTicker is a ticker of period d on the app's clock, for a loop written by
// hand ([Service.Go]): a test's manual clock drives it like every timer kit
// runs itself, where a time.Ticker would tick on the wall clock whatever the
// test does. It panics on a non-positive d, as time.NewTicker does.
func NewTicker(ctx context.Context, d time.Duration) clock.Ticker {
	return appClock(ctx).NewTicker(d)
}

// After is time.After on the app's clock.
func After(ctx context.Context, d time.Duration) <-chan time.Time {
	return appClock(ctx).After(d)
}

// appClock is the clock of the app ctx runs in, or the system clock outside
// one.
func appClock(ctx context.Context) clock.Timed {
	if a := appOf(ctx); a != nil && a.clock != nil {
		return a.clock
	}
	return clock.System
}

// Log returns the app's logger, for the node ctx runs inside. In dev, every
// record it writes inside a span is also kept beside that span, so the
// Studio shows a request's logs with its trace.
func Log(ctx context.Context) logger.Logger {
	if a := appOf(ctx); a != nil && a.log != nil {
		return a.productLog().With(logger.String("node", currentNode(ctx)))
	}
	return discardLogger()
}

// responseStateOf is the response state an endpoint's context carries, nil
// outside one.
func responseStateOf(ctx context.Context) *responseState {
	rs, _ := ctx.Value(responseKey{}).(*responseState)
	return rs
}

// withResponseState returns ctx carrying the response state of r.
func withResponseState(ctx context.Context, a *App, r *http.Request) (context.Context, *responseState) {
	rs := &responseState{clientIP: clientAddr(r, a.cfg.trustProxy), userAgent: clipRunes(r.UserAgent(), maxUserAgent)}
	return context.WithValue(ctx, responseKey{}, rs), rs
}

// add keeps a copy of c, unless the response is already written.
func (rs *responseState) add(c *http.Cookie) {
	cp := *c
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if !rs.sealed {
		rs.cookies = append(rs.cookies, &cp)
	}
}

// write adds the cookies to the response headers — before the status line —
// hardened, the last one set winning for a name, path and domain. A cookie
// net/http would refuse is dropped and logged, by name only: its value is
// often a secret.
func (rs *responseState) write(ctx context.Context, w http.ResponseWriter, a *App) {
	rs.mu.Lock()
	rs.sealed = true
	cookies := rs.cookies
	rs.mu.Unlock()
	if len(cookies) == 0 {
		return
	}
	dev := a.cfg.env == EnvDev
	var out []*http.Cookie
	slot := map[string]int{}
	for _, c := range cookies {
		h := harden(c, dev)
		if err := h.Valid(); err != nil {
			logger.Warn(ctx, a.log, "a cookie was dropped: its name, value, path or domain is not valid",
				logger.String("cookie", clip(h.Name)), logger.String("node", currentNode(ctx)))
			continue
		}
		id := h.Name + "\x00" + h.Path + "\x00" + h.Domain
		if i, ok := slot[id]; ok {
			out[i] = h
			continue
		}
		slot[id] = len(out)
		out = append(out, h)
	}
	for _, c := range out {
		w.Header().Add("Set-Cookie", c.String())
	}
}

// harden returns a copy of c with kit's defaults: Path "/", HttpOnly,
// SameSite Lax, and Secure outside dev.
func harden(c *http.Cookie, dev bool) *http.Cookie {
	h := *c
	if h.Path == "" {
		h.Path = "/"
	}
	h.HttpOnly = true
	if h.SameSite == 0 {
		h.SameSite = http.SameSiteLaxMode
	}
	if !dev || h.SameSite == http.SameSiteNoneMode {
		h.Secure = true
	}
	return &h
}

// clientAddr is the client of r: the connection's peer, or — behind a proxy
// the operator trusts — the last address of X-Forwarded-For, the one that
// proxy appended. Every address before it came from the client and may be
// forged: a client that sends "X-Forwarded-For: 6.6.6.6" arrives as
// "6.6.6.6, <its real address>". A forwarded value that is not an address
// is ignored.
func clientAddr(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
			// The last hop is the one the proxy added; an empty one names no
			// client, and the connection's address is used instead.
			hops := strings.Join(values, ",")
			if last := strings.TrimSpace(hops[strings.LastIndexByte(hops, ',')+1:]); last != "" {
				if ip, err := netip.ParseAddr(last); err == nil {
					return ip.Unmap().String()
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return host
}

// clipRunes bounds s to n runes.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
