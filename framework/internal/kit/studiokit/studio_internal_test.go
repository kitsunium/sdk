package studiokit

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// fakeHost is an app as the Studio's routes read it, answering what a test
// gives it; a method a test does not need is the embedded nil's, and panics.
type fakeHost struct {
	plug.StudioHost
	remote bool
	hosts  []string
	mails  []model.MailSummary
}

func (f *fakeHost) Remote() bool                  { return f.remote }
func (f *fakeHost) AllowedHost(host string) bool  { return slices.Contains(f.hosts, host) }
func (f *fakeHost) Mails(int) []model.MailSummary { return slices.Clone(f.mails) }
func (f *fakeHost) Stores() bool                  { return false }
func (f *fakeHost) NotFound(message string) error { return errors.New("not found: " + message) }
func (f *fakeHost) Invalid(message string) error  { return errors.New("invalid: " + message) }

func (f *fakeHost) Forbid(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) }

// Reply writes body, or an error's status: 404 for a not found, 400
// for anything else.
func (f *fakeHost) Reply(w http.ResponseWriter, _ *http.Request, body json.RawMessage, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if len(err.Error()) > 9 && err.Error()[:9] == "not found" {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		return
	}
	if _, err := w.Write(body); err != nil {
		return
	}
}

// The Host header is the client's to choose; the Studio also checks where the
// request comes from.
func TestStudioAnswersThisMachineOnly(t *testing.T) {
	h := &fakeHost{hosts: []string{"localhost", "127.0.0.1", "::1"}}
	s := studio{host: h}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for _, c := range []struct {
		remote, host string
		opt          bool
		want         int
	}{
		{"127.0.0.1:5555", "localhost:4000", false, http.StatusTeapot},
		{"[::1]:5555", "localhost:4000", false, http.StatusTeapot},
		{"192.168.1.9:5555", "localhost:4000", false, http.StatusForbidden},
		{"172.17.0.1:5555", "localhost:4000", true, http.StatusTeapot},
		{"127.0.0.1:5555", "evil.example:4000", false, http.StatusForbidden},
		{"127.0.0.1:5555", "[::1]:4000", false, http.StatusTeapot},
	} {
		h.remote = c.opt
		req := httptest.NewRequest("GET", "http://"+c.host+"/_kit/api/graph", nil)
		req.RemoteAddr = c.remote
		rec := httptest.NewRecorder()
		s.guard(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s as %s (opt-in %v): %d, want %d", c.remote, c.host, c.opt, rec.Code, c.want)
		}
		if c.want == http.StatusTeapot && rec.Header().Get("Content-Security-Policy") != studioCSP {
			t.Errorf("%s as %s: no content security policy", c.remote, c.host)
		}
	}
}

// Without a sampler — a product that imports no framework/kit/server — the
// dev route answers 404: kit samples nothing by itself (ADR 0172).
func TestNoSamplerNoProcess(t *testing.T) {
	sample := plug.SampleProcess.Load()
	plug.SampleProcess.Store(nil)
	t.Cleanup(func() { plug.SampleProcess.Store(sample) })
	rec := httptest.NewRecorder()
	studio{host: &fakeHost{}}.serveProcess(rec, httptest.NewRequest(http.MethodGet, "/_kit/api/process", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /_kit/api/process without a sampler: %d", rec.Code)
	}
}

// The mailbox merges every mailer's mail, newest first — the identifier
// breaking a tie —, and bounds its list.
func TestTheMailboxListsNewestFirst(t *testing.T) {
	at := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	h := &fakeHost{mails: []model.MailSummary{
		{ID: "m1", QueuedAt: at}, {ID: "m3", QueuedAt: at.Add(time.Minute)}, {ID: "m2", QueuedAt: at},
	}}
	s := studio{host: h}
	get := func(query string) (int, []model.MailSummary) {
		rec := httptest.NewRecorder()
		s.serveMails(rec, httptest.NewRequest(http.MethodGet, "/_kit/api/mail"+query, nil))
		var out []model.MailSummary
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, out
	}
	status, list := get("")
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if status != http.StatusOK || !slices.Equal(ids, []string{"m3", "m2", "m1"}) {
		t.Errorf("mail: %d %v", status, ids)
	}
	if status, list := get("?limit=1"); status != http.StatusOK || len(list) != 1 || list[0].ID != "m3" {
		t.Errorf("limit=1: %d %v", status, list)
	}
	for _, q := range []string{"?limit=0", "?limit=1001", "?limit=x"} {
		if status, _ := get(q); status != http.StatusBadRequest {
			t.Errorf("%s: %d", q, status)
		}
	}
}

// The source endpoint refuses, before it asks the app, a file it would not
// show: no name, an extension it does not serve, a path outside the root.
func TestTheSourceEndpointRefusesBeforeReading(t *testing.T) {
	s := studio{host: &fakeHost{}}
	for _, q := range []string{"", "?file=.env", "?file=../x.go", "?file=/etc/x.go", "?file=a.txt"} {
		rec := httptest.NewRecorder()
		s.serveSource(rec, httptest.NewRequest(http.MethodGet, "/_kit/api/source"+q, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: %d", q, rec.Code)
		}
	}
}
