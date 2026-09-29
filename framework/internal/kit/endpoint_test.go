package kit_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEndpointRoundTrip(t *testing.T) {
	app := start(t)
	it := create(t, app, "lamp", 3)
	if !strings.HasPrefix(it.ID, "item_") || it.State != Draft || it.Price != 10 {
		t.Fatalf("created %+v", it)
	}
	r := call(t, app, "GET /items/"+it.ID, noBody)
	var got Item
	r.json(t, &got)
	if r.status != http.StatusOK || got.Name != "lamp" {
		t.Fatalf("get: %d %s", r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if r.header.Get("X-Content-Type-Options") != "nosniff" || r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("missing safety headers: %v", r.header)
	}
	if r := call(t, app, "DELETE /items/"+it.ID, noBody); r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Errorf("delete of an Empty response: %d %q", r.status, r.body)
	}
	if r := call(t, app, "GET /items/"+it.ID, noBody); r.status != http.StatusNotFound || r.errorCode(t) != "not_found" {
		t.Errorf("get after delete: %d %s", r.status, r.body)
	}
}

// The route owns the fields it binds: a body cannot redirect a request to
// another entity.
func TestPathWinsOverBody(t *testing.T) {
	app := start(t)
	a := create(t, app, "a", 1)
	b := create(t, app, "b", 1)
	r := call(t, app, "PUT /items/"+a.ID, `{"name":"renamed","ID":"`+b.ID+`"}`)
	var got Item
	r.json(t, &got)
	if r.status != http.StatusOK || got.ID != a.ID || got.Name != "renamed" {
		t.Fatalf("PUT %s answered %d %s", a.ID, r.status, r.body)
	}
	r = call(t, app, "GET /items/"+b.ID, noBody)
	r.json(t, &got)
	if got.Name != "b" {
		t.Errorf("the body reached another entity: %+v", got)
	}
}

func TestQueryAndHeaderBinding(t *testing.T) {
	app := start(t)
	r := call(t, app, "GET /search?q=lamp&limit=7&since=2026-01-02T03:04:05Z&within=90s&tag=a&tag=b&exact=true", noBody)
	var got SearchInput
	r.json(t, &got)
	if r.status != http.StatusOK || got.Q != "lamp" || got.Limit != 7 || got.Within != 90*time.Second ||
		got.Since == nil || got.Since.Year() != 2026 || len(got.Tags) != 2 || !got.Exact {
		t.Fatalf("bound %+v from %s", got, r.body)
	}
	for _, bad := range []string{"limit=300", "limit=x", "since=yesterday", "within=forever", "exact=maybe"} {
		r := call(t, app, "GET /search?"+bad, noBody)
		if r.status != http.StatusBadRequest || r.errorCode(t) != "invalid_argument" {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
		if strings.Contains(string(r.body), strings.SplitN(bad, "=", 2)[1]) {
			t.Errorf("%s: the error echoes the value: %s", bad, r.body)
		}
	}
	r = call(t, app, "GET /whoami", noBody, "X-Agent", "tester")
	var who WhoInput
	r.json(t, &who)
	if who.Agent != "tester" {
		t.Errorf("header not bound: %s", r.body)
	}
}

func TestStrictBodies(t *testing.T) {
	app := start(t)
	cases := []struct {
		name, body, contentType string
		status                  int
		code                    string
	}{
		{"unknown member", `{"name":"x","price":1,"admin":true}`, "application/json", 400, "invalid_argument"},
		{"case-insensitive match refused", `{"Name":"x","price":1}`, "application/json", 400, "invalid_argument"},
		{"trailing data", `{"name":"x","price":1} {}`, "application/json", 400, "invalid_argument"},
		{"wrong type", `{"name":"x","price":"ten"}`, "application/json", 400, "invalid_argument"},
		{"int32 overflow", `{"name":"x","price":3000000000}`, "application/json", 400, "invalid_argument"},
		{"not json", `name=x`, "application/x-www-form-urlencoded", 415, "unsupported_media_type"},
		{"text/plain", `{"name":"x","price":1}`, "text/plain", 415, "unsupported_media_type"},
		{"vendor json accepted", `{"name":"ok","price":1}`, "application/vnd.shop+json", 200, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, reqErr := http.NewRequestWithContext(t.Context(), "POST", app.URL()+"/items", strings.NewReader(c.body))
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			req.Header.Set("Content-Type", c.contentType)
			resp, respErr := http.DefaultClient.Do(req)
			if respErr != nil {
				t.Fatal(respErr)
			}
			resp.Body.Close()
			if resp.StatusCode != c.status {
				t.Errorf("status %d, want %d", resp.StatusCode, c.status)
			}
		})
	}
	big := `{"note":"` + strings.Repeat("x", 100) + `"}`
	if r := call(t, app, "POST /limited", big); r.status != http.StatusRequestEntityTooLarge || r.errorCode(t) != "too_large" {
		t.Errorf("oversized body: %d %s", r.status, r.body)
	}
}

func TestValidationReportsEveryViolation(t *testing.T) {
	app := start(t)
	r := call(t, app, "POST /items", CreateInput{Name: "", Price: 0})
	var body struct {
		Error struct {
			Code       string `json:"code"`
			Violations []struct {
				Path, Rule, Message string
			} `json:"violations"`
		} `json:"error"`
	}
	r.json(t, &body)
	if r.status != http.StatusBadRequest || len(body.Error.Violations) != 2 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	paths := body.Error.Violations[0].Path + "," + body.Error.Violations[1].Path
	if !strings.Contains(paths, "name") || !strings.Contains(paths, "price") {
		t.Errorf("violations locate %s", paths)
	}
}

func TestFailuresSayNothingPrivate(t *testing.T) {
	app := start(t)
	r := call(t, app, "GET /boom", noBody)
	if r.status != http.StatusInternalServerError || r.errorCode(t) != "internal" {
		t.Fatalf("panic answered %d %s", r.status, r.body)
	}
	if strings.Contains(string(r.body), "do-not-leak-7f3a9c") || strings.Contains(string(r.body), "goroutine") {
		t.Fatalf("the panic leaked: %s", r.body)
	}
	r = call(t, app, "GET /slow", noBody)
	if r.status != http.StatusGatewayTimeout {
		t.Errorf("timeout answered %d %s", r.status, r.body)
	}
}

func TestRateLimit(t *testing.T) {
	app := start(t)
	codes := map[int]int{}
	for range 5 {
		codes[call(t, app, "POST /limited", LimitedInput{Note: "x"}).status]++
	}
	if codes[http.StatusOK] < 2 || codes[http.StatusTooManyRequests] == 0 {
		t.Fatalf("burst of 2 then refusals expected, got %v", codes)
	}
	if r := call(t, app, "POST /limited", LimitedInput{Note: "x"}); r.status == http.StatusTooManyRequests && r.errorCode(t) != "rate_limited" {
		t.Errorf("code %s", r.errorCode(t))
	}
}

func TestPrivateEndpointIsNotRouted(t *testing.T) {
	app := start(t)
	if r := call(t, app, "GET /internal/count", noBody); r.status != http.StatusNotFound {
		t.Fatalf("a private endpoint answered over HTTP: %d", r.status)
	}
	create(t, app, "x", 1)
	out, err := CountAPI.Call(t.Context(), struct{}{})
	if err != nil || out.Items != 1 {
		t.Fatalf("in-process call: %+v %v", out, err)
	}
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	app := start(t)
	r := call(t, app, "POST /items", CreateInput{Name: "csrf", Price: 1}, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
	if r.status != http.StatusForbidden {
		t.Fatalf("cross-site POST answered %d", r.status)
	}
	if r := call(t, app, "GET /search?q=x", noBody, "Sec-Fetch-Site", "cross-site"); r.status != http.StatusOK {
		t.Errorf("a cross-site GET is a read and must pass: %d", r.status)
	}
}
