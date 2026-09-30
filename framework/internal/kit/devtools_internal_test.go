package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/profiling"
)

// parkedLabels waits until a goroutine is parked in parkedIn and returns
// its labels once they equal want, or the last ones seen.
func parkedLabels(t *testing.T, want map[string]string) map[string]string {
	t.Helper()
	var got map[string]string
	found := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		gs, gsErr := profiling.Goroutines()
		if gsErr != nil {
			t.Fatal(gsErr)
		}
		for _, g := range gs {
			if slices.ContainsFunc(g.Stack, func(f profiling.Frame) bool { return strings.HasSuffix(f.Function, "kit.parkedIn") }) {
				got, found = g.Labels, true
			}
		}
		if found && (want == nil || equalLabels(got, want)) {
			return got
		}
	}
	if !found {
		t.Fatal("no goroutine parked in parkedIn")
	}
	return got
}

// parkedIn blocks in a function the goroutine dump can find by name, until
// told to go on.
//
//go:noinline
func parkedIn(step chan struct{}) { <-step }

// A span charges its goroutine to its node while it lasts, and gives the
// goroutine back its labels when it ends — nested spans included.
func TestLabelsAreRestoredWhenASpanEnds(t *testing.T) {
	svc := NewService("labels-probe", "")
	app := NewApp("labels", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())

	step := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		base := pprof.WithLabels(context.Background(), pprof.Labels("mine", "kept"))
		pprof.SetGoroutineLabels(base)
		ctx1, outer := app.begin(base, &spanStart{node: "labels-probe/endpoint/Outer", op: model.OpRun, name: "outer"})
		parkedIn(step)
		_, inner := app.begin(ctx1, &spanStart{node: "labels-probe/store/Inner", op: model.OpRead, name: "inner"})
		parkedIn(step)
		inner.end(nil)
		parkedIn(step)
		outer.end(nil)
		parkedIn(step)
	}()
	for i, want := range []map[string]string{
		{"mine": "kept", labelNode: "labels-probe/endpoint/Outer"},
		{"mine": "kept", labelNode: "labels-probe/store/Inner"},
		{"mine": "kept", labelNode: "labels-probe/endpoint/Outer"},
		{"mine": "kept"},
	} {
		if got := parkedLabels(t, want); !equalLabels(got, want) {
			t.Fatalf("step %d: labels %v, want %v", i, got, want)
		}
		step <- struct{}{}
	}
	<-done
}

func equalLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Production pays for no label: begin leaves the goroutine alone.
func TestProductionSetsNoLabel(t *testing.T) {
	svc := NewService("labels-prod", "")
	app := NewApp("labels", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	step := make(chan struct{})
	go func() {
		pprof.SetGoroutineLabels(context.Background())
		ctx, sp := app.begin(context.Background(), &spanStart{node: "labels-prod/endpoint/X", op: model.OpRun, name: "x"})
		if spanOf(ctx) != nil {
			t.Error("production keeps the span in the context")
		}
		parkedIn(step)
		sp.end(nil)
	}()
	if got := parkedLabels(t, map[string]string{}); len(got) != 0 {
		t.Errorf("production labelled the goroutine: %v", got)
	}
	step <- struct{}{}
}

func TestRedaction(t *testing.T) {
	type inner struct {
		Name   string `json:"name"`
		Recipe string `json:"recipe" kit:"secret"`
	}
	type Embedded struct {
		Pin string `json:"pin" kit:"other,secret"`
	}
	type outer struct {
		Embedded
		User     string            `json:"user"`
		Password string            `json:"password"`
		Items    []inner           `json:"items"`
		ByKey    map[string]inner  `json:"byKey"`
		Next     *outer            `json:"next,omitempty"`
		Cookie   string            `cookie:"sid"`
		Auth     string            `header:"X-Api-Key"`
		Accept   string            `header:"Accept"`
		Notes    map[string]string `json:"notes"`
		When     time.Time         `json:"when"`
	}
	v := outer{
		Pin: "1234", User: "ann", Password: "p", Items: []inner{{Name: "a", Recipe: "r1"}},
		ByKey: map[string]inner{"k": {Name: "b", Recipe: "r2"}}, Next: &outer{User: "bob", Items: []inner{{Recipe: "r3"}}},
		Cookie: "c", Auth: "key-9", Accept: "json", Notes: map[string]string{"refresh_token": "t", "ok": "fine"},
	}
	raw, cut := redactValue(v, payloadLimit)
	if cut {
		t.Fatal("cut a small value")
	}
	s := string(raw)
	for _, leaked := range []string{"1234", `"p"`, "r1", "r2", "r3", `"c"`, "key-9", `"t"`} {
		if strings.Contains(s, leaked) {
			t.Errorf("%s leaked: %s", leaked, s)
		}
	}
	for _, kept := range []string{`"user":"ann"`, `"name":"a"`, `"user":"bob"`, `"Accept":"json"`, `"ok":"fine"`} {
		if !strings.Contains(s, kept) {
			t.Errorf("%s is not a secret: %s", kept, s)
		}
	}

	// Names alone, on JSON the product did not type.
	named, _ := redactJSON([]byte(`{"Authorization":"Bearer x","nested":[{"apiKey":1,"session":{"id":2}}],"url":"https://u:pw@h/x"}`), payloadLimit)
	if string(named) != `{"Authorization":"[redacted]","nested":[{"apiKey":"[redacted]","session":"[redacted]"}],"url":"https://[redacted]@h/x"}` {
		t.Errorf("redacted %s", named)
	}
	// Cut, and still JSON.
	big := `{"a":"` + strings.Repeat("x", 100) + `","list":[` + strings.Repeat(`{"n":1},`, 2000) + `{"n":1}],"z":"end"}`
	short, cut := redactJSON([]byte(big), 1024)
	if !cut || !json.Valid(short) || len(short) > 1200 || bytes.Contains(short, []byte(`"z"`)) {
		t.Errorf("cut to %d bytes, valid %v, cut %v", len(short), json.Valid(short), cut)
	}
	raw, cut = redactJSON([]byte(`"`+strings.Repeat("é", 5000)+`"`), 1024)
	if !cut || !json.Valid(raw) || len(raw) > 1100 {
		t.Errorf("a long string cut to %d bytes, valid %v", len(raw), json.Valid(raw))
	}
	if malformed, _ := redactJSON([]byte(`{"a":`), 1024); string(malformed) != `"[not valid JSON]"` {
		t.Errorf("malformed input: %s", malformed)
	}
	if raw, _ := redactValue(func() {}, 1024); !json.Valid(raw) {
		t.Errorf("unencodable: %s", raw)
	}
}

// fakeMailbox is a mailer as the Studio's mailbox reads it.
type fakeMailbox struct {
	nodeBase
	sent []model.MailMessage
}

// : Asserts at compile time that the fake is read as the Studio reads a mailer.
var _ mailbox = (*fakeMailbox)(nil)

func (f *fakeMailbox) describe(*App, *model.Node) []model.Edge { return nil }

// mails returns the newest first, as the mailbox contract says.
func (f *fakeMailbox) mails(limit int) []model.MailSummary {
	out := make([]model.MailSummary, 0, len(f.sent))
	for _, m := range f.sent {
		out = append(out, m.MailSummary)
	}
	slices.SortFunc(out, func(x, y model.MailSummary) int { return y.QueuedAt.Compare(x.QueuedAt) })
	return out[:min(limit, len(out))]
}

func (f *fakeMailbox) mail(id string) (model.MailMessage, bool) {
	for _, m := range f.sent {
		if m.ID == id {
			return m, true
		}
	}
	return model.MailMessage{}, false
}

func TestMailboxReadsEveryMailer(t *testing.T) {
	svc := NewService("mailbox-probe", "")
	at := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	mk := func(id string, minutes int) model.MailMessage {
		return model.MailMessage{ID: id, Subject: id, Status: model.MailSent, QueuedAt: at.Add(time.Duration(minutes) * time.Minute), Text: "body of " + id}
	}
	one := &fakeMailbox{sent: []model.MailMessage{mk("m1", 1), mk("m3", 3)}}
	one.kind, one.name = model.KindMailer, "one"
	two := &fakeMailbox{sent: []model.MailMessage{mk("m2", 2)}}
	two.kind, two.name = model.KindMailer, "two"
	svc.add(one, true)
	svc.add(two, true)
	app := NewApp("mailbox", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	get := func(path string) (int, []byte) {
		req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+path, nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		resp, respErr := http.DefaultClient.Do(req)
		if respErr != nil {
			t.Fatal(respErr)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("closing the body: %v", err)
			}
		}()
		raw, rawErr := io.ReadAll(resp.Body)
		if rawErr != nil {
			t.Fatal(rawErr)
		}
		return resp.StatusCode, raw
	}
	getText := func(path string) (int, string) {
		status, raw := get(path)
		return status, string(raw)
	}
	status, raw := get("/_kit/api/mail")
	var list []model.MailSummary
	if err := json.Unmarshal(raw, &list); err != nil || status != 200 {
		t.Fatalf("mail: %d %s", status, raw)
	}
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"m3", "m2", "m1"}) {
		t.Errorf("newest first across mailers: %v", ids)
	}
	if status, body := getText("/_kit/api/mail?limit=1"); status != 200 || !strings.Contains(body, "m3") || strings.Contains(body, "m2") {
		t.Errorf("limit: %d %s", status, body)
	}
	status, raw = get("/_kit/api/mail/m2")
	var m model.MailMessage
	if err := json.Unmarshal(raw, &m); err != nil || status != 200 || m.Text != "body of m2" {
		t.Errorf("one mail: %d %s", status, raw)
	}
	for _, path := range []string{"/_kit/api/mail/nope", "/_kit/api/mail?limit=0"} {
		if status, _ := get(path); status != http.StatusNotFound && status != http.StatusBadRequest {
			t.Errorf("%s: %d", path, status)
		}
	}
}

// The process sample is the SDK's reading of this process.
func TestTheProcessSample(t *testing.T) {
	runtime.GC()
	p := sampleProcess()
	if p.PID != os.Getpid() || p.Goroutines < 1 || p.HeapBytes == 0 || p.MemoryBytes < p.HeapBytes || p.GCCycles == 0 || p.LastGC == nil {
		t.Errorf("sample %+v", p)
	}
	// The uptime and a pause's p99 may read 0 where the timer is coarse
	// (Windows: ~0.5-15 ms) or every pause is below the histogram's first
	// bucket: only a negative one is wrong.
	if p.UptimeMs < 0 || p.At.IsZero() || p.At.Location() != time.UTC || p.GCPauseP99Ms < 0 {
		t.Errorf("sample %+v", p)
	}
}

// An SMTP relay is a system of the context, located by its host and port
// and never by its credentials.
func TestArchitectureOfAnSMTPRelay(t *testing.T) {
	app := &App{name: "shop", cfg: config{env: EnvProduction}}
	g := &model.Graph{Nodes: []model.Node{
		{ID: "shop", Kind: model.KindService, Name: "shop"},
		{
			ID: "shop/mailer/mail", Kind: model.KindMailer, Name: "mail", Service: "shop",
			Mailer: &model.MailerInfo{Transport: model.TransportSMTP, Server: "smtp.example.com:587", TLS: "starttls", Outbox: "memory"},
		},
		{
			ID: "shop/mailer/alerts", Kind: model.KindMailer, Name: "alerts", Service: "shop",
			Mailer: &model.MailerInfo{Transport: model.TransportSMTP, Server: "relay.internal:25", TLS: "none"},
		},
	}}
	arch := app.architecture(g)
	if len(arch.Systems) != 2 {
		t.Fatalf("systems %+v", arch.Systems)
	}
	s := arch.Systems[1]
	if s.ID != "system:smtp:smtp.example.com:587" || s.Name != "SMTP relay" || s.Technology != "SMTP · STARTTLS" || s.Location != "smtp.example.com:587" || !slices.Equal(s.Nodes, []string{"shop/mailer/mail"}) {
		t.Errorf("relay %+v", s)
	}
	if arch.Systems[0].Technology != "SMTP · no TLS" {
		t.Errorf("relay %+v", arch.Systems[0])
	}
	var memory *model.Container
	for i := range arch.Containers {
		if arch.Containers[i].Kind == model.ContainerMemory {
			memory = &arch.Containers[i]
		}
	}
	if memory == nil || !slices.Contains(memory.Nodes, "shop/mailer/mail") {
		t.Errorf("a memory outbox lives in memory: %+v", arch.Containers)
	}
	if len(arch.People) != 0 {
		t.Errorf("no frontend, no auth: nobody %+v", arch.People)
	}
}

// GODEBUG=tracebacklabels=0 takes the labels out of the traceback; the dump
// still has them.
//
// Goroutine lifecycle: one goroutine parks until the deferred close of step
// releases it.
func TestTracebackWithoutLabels(t *testing.T) {
	t.Setenv("GODEBUG", "tracebacklabels=0")
	step := make(chan struct{})
	go pprof.Do(context.Background(), pprof.Labels(labelNode, "probe/endpoint/X"), func(context.Context) { parkedIn(step) })
	defer close(step)
	var buf strings.Builder
	eventually := func(cond func() bool) bool {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if cond() {
				return true
			}
		}
		return false
	}
	if !eventually(func() bool {
		buf.Reset()
		if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
			t.Fatal(err)
		}
		return strings.Contains(buf.String(), "kit.parkedIn")
	}) {
		t.Fatal("the goroutine never parked")
	}
	for _, g := range profiling.ParseGoroutines([]byte(buf.String())) {
		if len(g.Labels) > 0 {
			t.Fatalf("the traceback carries labels despite the GODEBUG: %+v", g)
		}
	}
	if got := parkedLabels(t, map[string]string{labelNode: "probe/endpoint/X"}); got[labelNode] != "probe/endpoint/X" {
		t.Fatalf("the dump lost the labels: %v", got)
	}
}
