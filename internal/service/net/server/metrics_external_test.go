// Package server_test — State's counters on a meter (#847).
package server_test

import (
	"bytes"
	"context"
	"io"
	stdnet "net"
	"regexp"
	"strconv"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// holdHandler keeps every connection open until its peer closes it.
type holdHandler struct{}

// ServeConn implements corenet.ConnHandler.
func (holdHandler) ServeConn(_ context.Context, c corenet.Conn) error {
	_, err := io.Copy(io.Discard, c)
	return err
}

// prometheusOf renders what m reports as the Prometheus text exposition.
func prometheusOf(t *testing.T, m coremetrics.FullMeter) string {
	t.Helper()
	var buf bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("prometheus", &buf).Export(m.Collect()); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.String()
}

// sample returns the value of the series named name whose label set holds
// every label of want, or -1 when there is none.
func sample(t *testing.T, doc, name string, want ...string) float64 {
	t.Helper()
	line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `(\{[^}]*\})? (\S+)$`)
	for _, m := range line.FindAllStringSubmatch(doc, -1) {
		ok := true
		for _, w := range want {
			ok = ok && regexp.MustCompile(regexp.QuoteMeta(w)).MatchString(m[1])
		}
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			t.Fatalf("%s: value %q: %v", name, m[2], err)
		}
		return v
	}
	return -1
}

// TestRegisterMetrics_prometheusCarriesTheGroupsCounters pins the audit's
// scenario: a group with a ceiling of 3 receives 4 connections, one of them
// turned away. After RegisterMetrics on a meter, a Prometheus export carries
// the issue's figures — net_server_conns_total 3, the connections the group
// admitted, net_server_conns_rejected_total 1, and 3 active — each carrying
// the group attribute and the caller's. State's TotalConns, which counts the
// refused connection too, reads 4: the two are not the same measure, and the
// export is held to the issue's numbers, not to State's.
func TestRegisterMetrics_prometheusCarriesTheGroupsCounters(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.MaxConns(3), server.Plaintext()).Handle(holdHandler{})
	meter := svcmetrics.NewMeter()
	if err := srv.RegisterMetrics(meter, coreotel.String("service", "edge")); err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address
	var conns []stdnet.Conn
	for range 4 {
		c, err := stdnet.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		conns = append(conns, c)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	if !waitFor(t, func() bool {
		st := srv.State()
		return st.TotalConns == 4 && st.RejectedConns == 1 && st.ActiveConns == 3
	}) {
		t.Fatalf("State never settled: %+v", srv.State())
	}
	doc := prometheusOf(t, meter)
	for _, c := range []struct {
		name string
		want float64
	}{
		{"net_server_conns_total", 3},
		{"net_server_conns_rejected_total", 1},
		{"net_server_conns_active", 3},
	} {
		if got := sample(t, doc, c.name, `group="api"`, `service="edge"`); got != c.want {
			t.Errorf("%s{group=api,service=edge} = %v, want %v\n%s", c.name, got, c.want, doc)
		}
	}
	for _, name := range []string{"net_server_accept_backoffs_total", "net_server_degraded"} {
		if got := sample(t, doc, name, `service="edge"`); got != 0 {
			t.Errorf("%s = %v, want 0 on a healthy server", name, got)
		}
	}
}

// TestRegisterMetrics_refusesNoMeterAndASecondRegistration pins the two
// refusals, both typed: no meter, and the same meter given the same server
// twice, which would run every callback twice per collection.
func TestRegisterMetrics_refusesNoMeterAndASecondRegistration(t *testing.T) {
	t.Parallel()
	srv := server.New()
	if err := srv.RegisterMetrics(nil); !errs.HasCode(err, corenet.CodeMetricsRefused) || !fieldIs(err, "why", "no meter") {
		t.Fatalf("RegisterMetrics(nil) = %v, want MetricsRefused, why=no meter", err)
	}
	meter := svcmetrics.NewMeter()
	if err := srv.RegisterMetrics(meter); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := srv.RegisterMetrics(meter); !errs.HasCode(err, corenet.CodeMetricsRefused) || !fieldIs(err, "why", "already registered") {
		t.Fatalf("second registration on the same meter = %v, want MetricsRefused, why=already registered", err)
	}
	//: another meter is another destination.
	if err := srv.RegisterMetrics(svcmetrics.NewMeter()); err != nil {
		t.Fatalf("registration on another meter: %v", err)
	}
}

// TestRegisterMetrics_namesAreTheDesignsConstants pins that every constant the
// design names is an instrument a registration creates, and nothing else is.
func TestRegisterMetrics_namesAreTheDesignsConstants(t *testing.T) {
	t.Parallel()
	srv := server.New()
	meter := svcmetrics.NewMeter()
	if err := srv.RegisterMetrics(meter); err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}
	doc := prometheusOf(t, meter)
	for _, name := range []string{
		server.MetricAcceptBackoffs, server.MetricDatagramsTruncated, server.MetricGSOOversized,
		server.MetricReplyFailures, server.MetricSuppressedLogs, server.MetricDegraded,
	} {
		if got := sample(t, doc, name); got != 0 {
			t.Errorf("%s = %v, want 0 on a new server\n%s", name, got, doc)
		}
	}
}
