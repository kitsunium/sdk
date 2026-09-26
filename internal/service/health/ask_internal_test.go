// Package health — the two halves of Ask no external test can see: the host
// it dials for a listen address, and the posture of the client it dials with.
package health

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestLoopbackForEveryUnspecifiedSpelling pins the listen-to-dial mapping over
// every spelling of "no particular address", in both families, and pins that
// every other host — an address, a zoned address, a name — is dialled as
// written.
func TestLoopbackForEveryUnspecifiedSpelling(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		host string
		want string
	}
	tests := []tc{
		{"empty", "", "127.0.0.1"},
		{"IPv4 unspecified", "0.0.0.0", "127.0.0.1"},
		{"IPv4-mapped unspecified", "::ffff:0.0.0.0", "127.0.0.1"},
		{"IPv6 unspecified", "::", "::1"},
		{"IPv6 unspecified, long form", "0:0:0:0:0:0:0:0", "::1"},
		{"IPv6 unspecified with a zone", "::%eth0", "::1"},
		{"IPv4 loopback", "127.0.0.1", "127.0.0.1"},
		{"another IPv4 address", "10.1.2.3", "10.1.2.3"},
		{"IPv6 loopback", "::1", "::1"},
		{"a zoned link-local address keeps its zone", "fe80::1%eth0", "fe80::1%eth0"},
		{"a name", "localhost", "localhost"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the host to dial.
		if got := loopbackFor(c.host); got != c.want {
			t.Errorf("loopbackFor(%q) = %q, want %q", c.host, got, c.want)
		}
	}
	//: one subtest per case.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDialTargetJoinsWhatItMapped pins that the port survives the mapping and
// that an IPv6 host is bracketed again.
func TestDialTargetJoinsWhatItMapped(t *testing.T) {
	t.Parallel()
	type tc struct {
		listen string
		want   string
	}
	tests := []tc{
		{":4000", "127.0.0.1:4000"},
		{"0.0.0.0:4000", "127.0.0.1:4000"},
		{"[::]:4000", "[::1]:4000"},
		{"127.0.0.1:4000", "127.0.0.1:4000"},
		{"localhost:4000", "localhost:4000"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := dialTarget(c.listen)
		//: a usable listen address.
		if err != nil || got != c.want {
			t.Errorf("dialTarget(%q) = %q, %v; want %q", c.listen, got, err, c.want)
		}
	}
	//: one subtest per case.
	for _, c := range tests {
		t.Run(c.listen, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAskClientNeverProxiesAndKeepsNothing pins the client's posture, which no
// loopback test can show: net/http never proxies a loopback address anyway,
// so only the transport itself says whether a named host would go through
// HTTP_PROXY. DefaultClient would, would keep the connection, and would
// follow a redirect.
func TestAskClientNeverProxiesAndKeepsNothing(t *testing.T) {
	t.Parallel()
	client := askClient()
	transport, isTransport := client.Transport.(*http.Transport)
	//: a transport of its own, never the shared default.
	if !isTransport || transport == http.DefaultTransport {
		t.Fatalf("the client's transport is %T, want a fresh *http.Transport", client.Transport)
	}
	//: no proxy, whatever the environment says.
	if transport.Proxy != nil {
		t.Error("the transport consults a proxy")
	}
	//: no connection kept once the Ask returns.
	if !transport.DisableKeepAlives {
		t.Error("the transport keeps connections alive")
	}
	//: a header bounded far under net/http's default.
	if transport.MaxResponseHeaderBytes != maxAskHeaderBytes {
		t.Errorf("header bound = %d, want %d", transport.MaxResponseHeaderBytes, maxAskHeaderBytes)
	}
	//: a redirect is returned, not followed.
	if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Error("the client follows redirects")
	}
}

// stallingBody is a response body that sends nothing and blocks until the
// request's context ends: a process that answered its status line and then
// stopped sending. started closes when the first Read begins, so a test knows
// the drain is under way before it ends the bound.
type stallingBody struct {
	ctx     context.Context
	started chan struct{}
}

// Read signals the drain began, then blocks until the request's context ends.
func (b *stallingBody) Read([]byte) (int, error) {
	//: first read: the drain is running.
	select {
	//: already signalled.
	case <-b.started:
	//: the first read: signal it.
	default:
		close(b.started)
	}
	<-b.ctx.Done()
	return 0, context.Cause(b.ctx)
}

// Close closes nothing.
func (*stallingBody) Close() error { return nil }

// stallingTransport answers every request with 200 and a stallingBody.
type stallingTransport struct {
	started chan struct{}
}

// RoundTrip answers 200 at once, with a body that never arrives.
func (t stallingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       &stallingBody{ctx: request.Context(), started: t.started},
		Request:    request,
	}, nil
}

// TestAStalledBodyIsATimeoutNotAnAnswer pins what "bounded" means for the
// body: a process that sends 200 and then stops sending has not answered
// within the bound, so when the budget ends during the drain the verdict is
// ASK_TIMEOUT with no status — not the status line it managed to send.
//
// Goroutine lifecycle: one goroutine ends the bound once the drain has begun;
// it exits right after, and the test waits for the exchange to return.
func TestAStalledBodyIsATimeoutNotAnAnswer(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	client := &http.Client{Transport: stallingTransport{started: started}, CheckRedirect: keepRedirect}
	request := askRequest{url: "http://127.0.0.1:4000/readyz", target: "127.0.0.1:4000", budget: DefaultAskTimeout}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() {
		//: the drain is blocked on the stalled body; now the budget ends.
		<-started
		cancel(AskTimeout)
	}()
	status, err := request.exchange(ctx, client)
	//: no whole answer within the bound: no status, and the budget as the reason.
	if status != 0 || !errors.Is(err, AskTimeout) {
		t.Fatalf("exchange() = %d, %v; want 0 and ASK_TIMEOUT", status, err)
	}
}

// TestADrainCutByTheBoundOnlyIsATimeout pins the other side: a body that ends
// on its own — shorter than MaxAskDrainBytes, or cut at it — leaves a 200 a
// 200, and so does a drain that failed while the bound was still running.
func TestADrainCutByTheBoundOnlyIsATimeout(t *testing.T) {
	t.Parallel()
	request := askRequest{url: "http://127.0.0.1:4000/readyz", target: "127.0.0.1:4000", budget: DefaultAskTimeout}
	client := &http.Client{Transport: failingBodyTransport{}, CheckRedirect: keepRedirect}
	status, err := request.exchange(t.Context(), client)
	//: the status line is the answer when the bound did not end the drain.
	if status != http.StatusOK || err != nil {
		t.Fatalf("exchange() = %d, %v; want 200 and nil", status, err)
	}
}

// failingBodyTransport answers 200 with a body whose read fails at once, while
// the request's context is still live: a connection the server reset mid-body.
type failingBodyTransport struct{}

// RoundTrip answers 200 with a body that fails.
func (failingBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: failingBody{}, Request: request}, nil
}

// failingBody fails every read.
type failingBody struct{}

// Read fails as a reset connection does.
func (failingBody) Read([]byte) (int, error) { return 0, errReset }

// Close closes nothing.
func (failingBody) Close() error { return nil }

// errReset stands for a connection the peer reset while sending the body.
var errReset = errors.New("read: connection reset by peer")
