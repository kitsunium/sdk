// Package health — hosts Ask, the client half of a readiness probe: the
// question a container's HEALTHCHECK asks a running process, from an image
// that has no shell and no curl to ask it with (ADR 0131).
package health

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultAskTimeout is the budget an Ask gets when [AskConfig.Timeout] is zero.
//
// Three seconds sits far above what a readiness endpoint answering from
// memory needs, and an order of magnitude under Docker's own HEALTHCHECK
// timeout of thirty seconds: a probe that cannot get an answer reports why,
// with its own code, before the supervisor gives up on the command itself.
const DefaultAskTimeout time.Duration = 3 * time.Second

// MaxAskDrainBytes is how much of a response body Ask reads before closing it.
//
// Reading a short answer to its end lets the server finish writing instead of
// meeting a reset on every probe; the bound stops an endless or hostile body
// from holding the probe once the status, which is the whole answer, is in.
const MaxAskDrainBytes int64 = 64 << 10

// maxAskHeaderBytes bounds the response header the transport accepts, far
// under net/http's default: a readiness answer's header is a few lines.
const maxAskHeaderBytes int64 = 64 << 10

// highestPort is the largest TCP port number.
const highestPort int = 65535

// Field names the Ask refusals carry.
const (
	// fieldArgument names the AskConfig field a misconfigured Ask got wrong.
	fieldArgument string = "argument"
	// fieldTarget carries the host:port the Ask dialled.
	fieldTarget string = "target"
	// fieldBudget carries the budget an Ask ran out of.
	fieldBudget string = "budget"
	// fieldStatus carries the status the process answered.
	fieldStatus string = "status"
)

// AskConfig says where a process listens and which path answers whether it is
// ready. Addr and Path are required; Timeout and Clock have working zeros.
type AskConfig struct {
	// Addr is the address the process LISTENS on, spelled as its listener was
	// given it: ":4000", "0.0.0.0:4000", "[::]:4000", "127.0.0.1:4000",
	// "localhost:4000". An unspecified host is not an address one can dial,
	// so it becomes this machine's loopback of the same family: an empty host
	// and 0.0.0.0 dial 127.0.0.1, :: dials ::1. Any other host is dialled as
	// written. The port must be a number from 1 to 65535.
	Addr string
	// Path is the readiness endpoint, absolute and with an optional query:
	// "/readyz", "/_kit/health/ready".
	Path string
	// Timeout bounds the whole exchange: dial, request, answer and drained
	// body. Zero is [DefaultAskTimeout]; a negative value is refused. The
	// caller's context bounds the exchange too, and whichever ends first ends
	// it.
	Timeout time.Duration
	// Clock is the time source the budget is armed on; nil is clock.System. A
	// manual clock makes the timeout a test can reach without waiting.
	Clock clock.Timed
}

// askRequest is an AskConfig that passed validation: the URL to GET, the
// host:port it names, the budget and the clock that measures it.
type askRequest struct {
	url    string
	target string
	budget time.Duration
	clk    clock.Timed
}

// Ask asks the process listening on cfg.Addr whether it is ready, as a
// container's HEALTHCHECK does: one GET of cfg.Path over plain HTTP. It
// returns the status the process answered — zero when it answered nothing —
// and a nil error exactly when that status is 200.
//
// Otherwise the error says why, by code:
//
//   - [AskMisconfigured]: cfg cannot reach anything; nothing was dialled.
//   - [AskUnreachable]: the connection or the request failed; the
//     transport's error is the cause.
//   - [AskTimeout]: the budget, or the caller's context, ended before an
//     answer; a caller's own context error stays in the chain.
//   - [AskNotReady]: the process answered another status, carried by the
//     status field. A redirect is such an answer: none is followed, so a
//     probe cannot be sent elsewhere.
//
// The exchange never goes through a proxy, whatever the environment says, and
// keeps no connection alive. At most [MaxAskDrainBytes] of the body are read,
// then the body is closed; no byte of it ever reaches an error.
func Ask(ctx context.Context, cfg AskConfig) (status int, err error) {
	request, err := cfg.resolve()
	//: a configuration no answer can satisfy is refused before dialling.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return 0, err
	}
	askCtx, cancel := context.WithCancelCause(ctx)
	//: every path out releases the context.
	defer cancel(nil)
	release := armAsk(request.clk, request.budget, cancel)
	//: the budget's goroutine ends with the Ask.
	defer release()
	//: the status, and why it is not 200 when it is not.
	return request.send(askCtx)
}

// resolve validates cfg and fills its zeros.
func (c AskConfig) resolve() (askRequest, error) {
	//: a budget below zero has no reading that is not a bug.
	if c.Timeout < 0 {
		//: AskMisconfigured, naming the argument.
		return askRequest{}, askMisconfigured("timeout")
	}
	target, err := dialTarget(c.Addr)
	//: nothing to dial.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return askRequest{}, err
	}
	requestURL, err := absoluteURL(target, c.Path)
	//: nothing to ask for.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return askRequest{}, err
	}
	budget := c.Timeout
	//: zero is the default budget, never "no time at all" (ADR 0031).
	if budget == 0 {
		budget = DefaultAskTimeout
	}
	clk := c.Clock
	//: the wall clock unless a test brought its own.
	if clk == nil {
		clk = clock.System
	}
	//: ready to send.
	return askRequest{url: requestURL, target: target, budget: budget, clk: clk}, nil
}

// dialTarget turns the address a process listens on into the host:port this
// process dials to reach it.
func dialTarget(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	//: not a host:port at all.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return "", askMisconfigured("addr")
	}
	number, err := strconv.Atoi(port)
	//: a port nothing can be listening on: absent, named, zero or too high.
	if err != nil || number < 1 || number > highestPort {
		//: AskMisconfigured, naming the argument.
		return "", askMisconfigured("addr")
	}
	//: the loopback stands in for an unspecified host.
	return net.JoinHostPort(loopbackFor(host), port), nil
}

// loopbackFor returns the host to dial for a listener's host: this machine's
// loopback of the same family when the listener named no particular address,
// the host itself otherwise.
func loopbackFor(host string) string {
	//: ":4000" — every address; IPv4's loopback is the one a dual-stack
	//: listener and an IPv4-only one both accept.
	if host == "" {
		//: IPv4 loopback.
		return "127.0.0.1"
	}
	addr, err := netip.ParseAddr(host)
	//: a name — "localhost", a host name — is dialled as written.
	if err != nil {
		//: resolved by the dialer.
		return host
	}
	unzoned := addr.WithZone("").Unmap()
	//: a particular address is dialled as written, zone included.
	if !unzoned.IsUnspecified() {
		//: as given.
		return host
	}
	//: 0.0.0.0, and its IPv4-mapped spelling.
	if unzoned.Is4() {
		//: IPv4 loopback.
		return "127.0.0.1"
	}
	//: :: — IPv6's loopback.
	return "::1"
}

// absoluteURL builds the URL an Ask gets: plain HTTP to target, at path.
func absoluteURL(target, path string) (string, error) {
	//: a relative path would be resolved against nothing.
	if !strings.HasPrefix(path, "/") {
		//: AskMisconfigured, naming the argument.
		return "", askMisconfigured("path")
	}
	parsed, err := url.ParseRequestURI(path)
	//: a path net/url cannot carry, such as one holding a control byte.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return "", askMisconfigured("path")
	}
	built := url.URL{Scheme: "http", Host: target, Path: parsed.Path, RawPath: parsed.RawPath, RawQuery: parsed.RawQuery}
	//: the host is always target: a path cannot name another one.
	return built.String(), nil
}

// armAsk arms the budget on the injected clock: when it fires first, the Ask's
// context is cancelled with AskTimeout as its cause. It returns the release
// the Ask runs on its way out.
//
// # Goroutine lifetime
//
// One goroutine per Ask. It ends at the budget, having cancelled, or at the
// release, whichever comes first.
func armAsk(clk clock.Timed, budget time.Duration, cancel context.CancelCauseFunc) (release func()) {
	//: armed on the INJECTED clock, like every other budget in this package.
	timer := clk.NewTimer(budget)
	released := make(chan struct{})
	go func() {
		defer timer.Stop()
		select {
		//: no answer within the budget.
		case <-timer.C():
			//: end the exchange, and say why.
			cancel(AskTimeout)
		//: the Ask returned first.
		case <-released:
			//: nothing to end.
		}
	}()
	//: closed once, by the Ask's deferred call.
	return func() { close(released) }
}

// askClient builds the client of one Ask: a fresh transport that never
// proxies, keeps nothing alive and bounds the header, and no redirect
// followed.
func askClient() *http.Client {
	transport := &http.Transport{
		// Proxy is left nil on purpose: a probe of this machine must never
		// leave it through whatever HTTP_PROXY the environment names.
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxAskHeaderBytes,
	}
	//: one client, one exchange.
	return &http.Client{Transport: transport, CheckRedirect: keepRedirect}
}

// send performs the exchange: one GET on askClient.
func (r askRequest) send(ctx context.Context) (status int, err error) {
	client := askClient()
	//: no connection outlives the Ask.
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	//: unreachable: the URL was built from a validated target and path.
	if err != nil {
		//: AskMisconfigured, naming the argument.
		return 0, askMisconfigured("path")
	}
	response, err := client.Do(request)
	//: no answer at all.
	if err != nil {
		//: AskTimeout or AskUnreachable.
		return 0, r.unanswered(ctx, err)
	}
	discard(response.Body)
	//: 200 is ready, and only 200.
	if response.StatusCode != http.StatusOK {
		//: AskNotReady, carrying the status and nothing of the body.
		return response.StatusCode, kerrs.Wrap(AskNotReady, kerrs.WrapParams{},
			kerrs.String(fieldTarget, r.target), kerrs.Int(fieldStatus, response.StatusCode))
	}
	//: ready.
	return http.StatusOK, nil
}

// keepRedirect answers a redirect with the redirect itself: the probe asked
// this process, and a Location naming another is an answer, not a new address.
// Neither the next request nor the ones before it are consulted: every
// redirect is refused alike.
func keepRedirect(next *http.Request, previous []*http.Request) error {
	//: net/http's own spelling of "stop, and return the response".
	return http.ErrUseLastResponse
}

// discard reads at most MaxAskDrainBytes of body and closes it. What the
// drain or the close return changes nothing: the status is already the
// answer, and the body is never read into anything.
func discard(body io.ReadCloser) {
	//: a failed or cut drain only means the connection closes less politely.
	if _, err := io.CopyN(io.Discard, body, MaxAskDrainBytes); err != nil && !errors.Is(err, io.EOF) {
		//: the close below runs either way.
	}
	//: a close failure leaves nothing to release: keep-alive is off.
	if err := body.Close(); err != nil {
		//: nothing held.
	}
}

// unanswered classifies an exchange that produced no answer.
func (r askRequest) unanswered(ctx context.Context, doErr error) error {
	target := kerrs.String(fieldTarget, r.target)
	//: this Ask's own budget ended it.
	if errors.Is(context.Cause(ctx), AskTimeout) {
		//: AskTimeout, with the budget it ran out of.
		return kerrs.Wrap(AskTimeout, kerrs.WrapParams{}, target, kerrs.String(fieldBudget, r.budget.String()))
	}
	//: the caller's own context ended first: its error stays in the chain.
	if ctx.Err() != nil {
		//: AskTimeout, caused by the caller's context.
		return kerrs.Wrap(context.Cause(ctx), paramsOf(AskTimeout,
			"service/health: the caller's context ended before the process answered"), target)
	}
	//: the network's own timeout — a dial the kernel gave up on.
	if netErr, isNet := errors.AsType[net.Error](doErr); isNet && netErr.Timeout() {
		//: AskTimeout, caused by the transport's error.
		return kerrs.Wrap(doErr, paramsOf(AskTimeout,
			"service/health: the network timed out before the process answered"), target)
	}
	//: refused, reset, unroutable, unresolvable.
	return kerrs.Wrap(doErr, paramsOf(AskUnreachable, AskUnreachable.Private()), target)
}

// sentinelIdentity is what paramsOf reads from a sentinel: everything but its
// Private.
type sentinelIdentity interface {
	Code() kerrs.Code
	Reason() string
	Public() string
	ExitCode() int
}

// paramsOf reads a sentinel's identity into WrapParams, with a Private of its
// own, so a plain cause can be wrapped under that sentinel's code.
func paramsOf(sentinel sentinelIdentity, private string) kerrs.WrapParams {
	//: read from the sentinel so the identity cannot drift from it.
	return kerrs.WrapParams{
		Code:     sentinel.Code(),
		Reason:   sentinel.Reason(),
		Public:   sentinel.Public(),
		Private:  private,
		ExitCode: sentinel.ExitCode(),
	}
}

// askMisconfigured refuses an Ask before anything is dialled.
func askMisconfigured(argument string) error {
	//: the argument's name, never its value.
	return kerrs.Wrap(AskMisconfigured, kerrs.WrapParams{}, kerrs.String(fieldArgument, argument))
}
