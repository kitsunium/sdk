// Package static_test — the handler as a server meets it: what it serves,
// what it refuses, and the headers every answer carries.
package static_test

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/static"
)

// site is the tree most tests serve: a page, a hashed asset directory with no
// index, a documentation directory with one, a nested page.
func site() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  {Data: []byte("<!doctype html><title>shell</title>")},
		"app.js":                      {Data: []byte("console.log('app')")},
		"assets/app-3f2a9c.js":        {Data: []byte("console.log('hashed')")},
		"assets/style-77e0b1.css":     {Data: []byte("body{}")},
		"guide/index.html":            {Data: []byte("<!doctype html><title>guide</title>")},
		"guide/chapter.html":          {Data: []byte("<!doctype html><title>chapter</title>")},
		"docs/a.txt":                  {Data: []byte("listed-name-a")},
		"docs/b.txt":                  {Data: []byte("listed-name-b")},
		".well-known/security.txt":    {Data: []byte("Contact: mailto:security@example.com")},
		"nested/deeper/leaf.json":     {Data: []byte(`{"leaf":true}`)},
		"pipe":                        {Mode: fs.ModeNamedPipe},
		"guide-without-index/x.html":  {Data: []byte("<p>x</p>")},
		"index.html.bak/unreadable.x": {Data: []byte("never")},
	}
}

// build returns a handler over fsys, failing the test on a refusal.
func build(t *testing.T, fsys fs.FS, cfg static.Config) *static.Handler {
	t.Helper()
	handler, err := static.NewHandler(fsys, cfg)
	//: every configuration here is valid.
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

// get sends one request through handler and returns the recorded response.
func get(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

// TestServesFilesAndDirectoriesByTheirIndex pins what a request resolves to:
// a file, a directory's index.html after a redirect to its slashed name, the
// root's index, and 404 for a directory with no index — never a listing.
func TestServesFilesAndDirectoriesByTheirIndex(t *testing.T) {
	t.Parallel()
	handler := build(t, site(), static.Config{})
	type tc struct {
		name         string
		target       string
		wantStatus   int
		wantBody     string
		wantLocation string
		forbidden    []string
	}
	tests := []tc{
		{name: "a file", target: "/app.js", wantStatus: http.StatusOK, wantBody: "console.log('app')"},
		{name: "a file with a query", target: "/app.js?v=3", wantStatus: http.StatusOK, wantBody: "console.log('app')"},
		{name: "a nested file", target: "/nested/deeper/leaf.json", wantStatus: http.StatusOK, wantBody: `{"leaf":true}`},
		{name: "the root", target: "/", wantStatus: http.StatusOK, wantBody: "<title>shell</title>"},
		{name: "the root's index by name", target: "/index.html", wantStatus: http.StatusOK, wantBody: "<title>shell</title>"},
		{name: "a directory with an index", target: "/guide/", wantStatus: http.StatusOK, wantBody: "<title>guide</title>"},
		{name: "a directory named without its slash", target: "/guide", wantStatus: http.StatusMovedPermanently, wantLocation: "./guide/"},
		{name: "the redirect keeps the query", target: "/guide?tab=2", wantStatus: http.StatusMovedPermanently, wantLocation: "./guide/?tab=2"},
		{name: "a directory without an index is not listed", target: "/docs/", wantStatus: http.StatusNotFound, forbidden: []string{"a.txt", "b.txt"}},
		{name: "nor without its slash", target: "/docs", wantStatus: http.StatusNotFound, forbidden: []string{"a.txt", "b.txt"}},
		{name: "a dot directory's file", target: "/.well-known/security.txt", wantStatus: http.StatusOK, wantBody: "Contact:"},
		{name: "a file asked for as a directory", target: "/app.js/", wantStatus: http.StatusNotFound},
		{name: "a name that names nothing", target: "/missing.js", wantStatus: http.StatusNotFound},
		{name: "a named pipe", target: "/pipe", wantStatus: http.StatusNotFound},
		{name: "a directory named index.html.bak is not an index", target: "/index.html.bak/", wantStatus: http.StatusNotFound},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		response := get(handler, http.MethodGet, c.target)
		//: the status first.
		if response.Code != c.wantStatus {
			t.Fatalf("GET %s = %d, want %d (body %q)", c.target, response.Code, c.wantStatus, response.Body.String())
		}
		//: the file's bytes.
		if c.wantBody != "" && !strings.Contains(response.Body.String(), c.wantBody) {
			t.Errorf("GET %s body = %q, want it to hold %q", c.target, response.Body.String(), c.wantBody)
		}
		//: where a redirect sends.
		if got := response.Header().Get("Location"); got != c.wantLocation {
			t.Errorf("GET %s Location = %q, want %q", c.target, got, c.wantLocation)
		}
		//: nothing of what a listing would show.
		for _, word := range c.forbidden {
			//: the body names no entry.
			if strings.Contains(response.Body.String(), word) {
				t.Errorf("GET %s listed the directory: %q", c.target, response.Body.String())
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheRootIsServedWhenStripPrefixLeavesNothing pins the empty path
// http.StripPrefix hands over for a request to the bare prefix: the root's
// index, not a redirect this handler cannot aim.
func TestTheRootIsServedWhenStripPrefixLeavesNothing(t *testing.T) {
	t.Parallel()
	handler := http.StripPrefix("/app", build(t, site(), static.Config{}))
	response := get(handler, http.MethodGet, "/app")
	//: the index, whole.
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>shell</title>") {
		t.Fatalf("GET /app = %d %q, want 200 and the root's index", response.Code, response.Body.String())
	}
}

// TestTheDirectoryRedirectStaysUnderAPrefix pins why the Location is relative:
// behind http.StripPrefix the handler never sees "/app", and an absolute
// Location built from the path it does see would leave the prefix.
func TestTheDirectoryRedirectStaysUnderAPrefix(t *testing.T) {
	t.Parallel()
	handler := http.StripPrefix("/app", build(t, site(), static.Config{}))
	response := get(handler, http.MethodGet, "/app/guide")
	//: a redirect.
	if response.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /app/guide = %d, want 301", response.Code)
	}
	base, err := url.Parse("https://example.com/app/guide")
	//: a fixed, valid URL.
	if err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	//: a parsable Location.
	if err != nil {
		t.Fatalf("Location %q: %v", response.Header().Get("Location"), err)
	}
	//: resolved as a browser resolves it: the same host, under the prefix.
	if got := base.ResolveReference(location).String(); got != "https://example.com/app/guide/" {
		t.Errorf("the redirect resolves to %s, want https://example.com/app/guide/", got)
	}
}

// escapingFS is the file system a traversal needs: it joins a name onto its
// root with path.Join, which cleans "../" upward, and never checks the name.
// Behind a handler that passed a raw path, "../secret.txt" would read the
// file outside the root.
type escapingFS struct {
	whole fstest.MapFS
	root  string
	mu    sync.Mutex
	asked []string
}

// Open records the name and serves whatever joining it onto root names.
func (e *escapingFS) Open(name string) (fs.File, error) {
	e.mu.Lock()
	e.asked = append(e.asked, name)
	e.mu.Unlock()
	//: path.Join cleans, so a leading ".." climbs out of root.
	return e.whole.Open(path.Join(e.root, name))
}

// names returns every name the handler asked for.
func (e *escapingFS) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.asked)
}

// TestNoRequestClimbsAboveTheRoot pins the traversal refusal against a file
// system that would follow one: every spelling of ".." is cleaned from the
// root first, so the secret beside the tree is never served, and every name
// the file system is asked for is a valid io/fs name.
func TestNoRequestClimbsAboveTheRoot(t *testing.T) {
	t.Parallel()
	tree := &escapingFS{root: "public", whole: fstest.MapFS{
		"secret.txt":        {Data: []byte("TOP-SECRET-7d41")},
		"public/index.html": {Data: []byte("<title>public</title>")},
	}}
	handler := build(t, tree, static.Config{SinglePageApp: true})
	for _, target := range []string{
		"/../secret.txt",
		"/a/../../secret.txt",
		"/./../secret.txt",
		"/%2e%2e/secret.txt",
		"/..%2fsecret.txt",
		"//../secret.txt",
		"/public/../../secret.txt",
	} {
		response := get(handler, http.MethodGet, target)
		//: never the file outside the tree.
		if strings.Contains(response.Body.String(), "TOP-SECRET") {
			t.Errorf("GET %s served the file above the root", target)
		}
	}
	//: the file system only ever saw names inside the tree.
	for _, name := range tree.names() {
		//: valid, so no ".." and no leading slash.
		if !fs.ValidPath(name) {
			t.Errorf("the file system was asked for %q, which is not a valid io/fs name", name)
		}
	}
}

// TestTheSinglePageFallbackServesOnlyRoutes pins the fallback: a path with no
// extension that names nothing gets the root's index with 200; a missing
// asset stays a 404, so a script tag never receives HTML; a directory without
// an index stays a 404; without the option, a route is a 404.
func TestTheSinglePageFallbackServesOnlyRoutes(t *testing.T) {
	t.Parallel()
	spa := build(t, site(), static.Config{SinglePageApp: true})
	plain := build(t, site(), static.Config{})
	type tc struct {
		name       string
		handler    http.Handler
		target     string
		wantStatus int
		wantShell  bool
	}
	tests := []tc{
		{"a client-side route", spa, "/settings/profile", http.StatusOK, true},
		{"a route with a trailing slash", spa, "/settings/", http.StatusOK, true},
		{"a route with a dotted segment", spa, "/v1.2/page", http.StatusOK, true},
		{"a missing script", spa, "/missing.js", http.StatusNotFound, false},
		{"a missing hashed asset", spa, "/assets/app-000000.js", http.StatusNotFound, false},
		{"a directory without an index", spa, "/docs/", http.StatusNotFound, false},
		{"a file is still the file", spa, "/app.js", http.StatusOK, false},
		{"without the option, a route is a 404", plain, "/settings/profile", http.StatusNotFound, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		response := get(c.handler, http.MethodGet, c.target)
		//: the status.
		if response.Code != c.wantStatus {
			t.Fatalf("GET %s = %d, want %d", c.target, response.Code, c.wantStatus)
		}
		//: the shell where a route was asked, and nowhere else.
		if shell := strings.Contains(response.Body.String(), "<title>shell</title>"); shell != c.wantShell {
			t.Errorf("GET %s served the shell: %v, want %v", c.target, shell, c.wantShell)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheFallbackIsA404WithoutAShell pins that a tree with no index.html at
// its root has no shell to fall back to.
func TestTheFallbackIsA404WithoutAShell(t *testing.T) {
	t.Parallel()
	handler := build(t, fstest.MapFS{"app.js": {Data: []byte("x")}}, static.Config{SinglePageApp: true})
	//: nothing to serve for a route.
	if response := get(handler, http.MethodGet, "/settings"); response.Code != http.StatusNotFound {
		t.Fatalf("GET /settings = %d, want 404", response.Code)
	}
}

// TestEveryResponseCarriesTheSecurityHeaders pins the headers on every kind of
// answer — a file, a redirect, a 404, a 405, a 500 — with the defaults and
// with a configuration of the caller's own.
func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	t.Parallel()
	tree := failingFS{MapFS: site(), openFails: "broken.js"}
	defaults := build(t, tree, static.Config{})
	custom := build(t, tree, static.Config{
		ContentSecurityPolicy: "default-src 'self'; img-src 'self' data:",
		ReferrerPolicy:        "no-referrer",
	})
	type tc struct {
		name         string
		handler      http.Handler
		method       string
		target       string
		wantStatus   int
		wantCSP      string
		wantReferrer string
	}
	csp, referrer := static.DefaultContentSecurityPolicy, static.DefaultReferrerPolicy
	ownCSP, ownReferrer := "default-src 'self'; img-src 'self' data:", "no-referrer"
	tests := []tc{
		{"a file", defaults, http.MethodGet, "/app.js", http.StatusOK, csp, referrer},
		{"a redirect", defaults, http.MethodGet, "/guide", http.StatusMovedPermanently, csp, referrer},
		{"a 404", defaults, http.MethodGet, "/missing", http.StatusNotFound, csp, referrer},
		{"a 405", defaults, http.MethodPost, "/app.js", http.StatusMethodNotAllowed, csp, referrer},
		{"a 500", defaults, http.MethodGet, "/broken.js", http.StatusInternalServerError, csp, referrer},
		{"HEAD", defaults, http.MethodHead, "/app.js", http.StatusOK, csp, referrer},
		{"a file, own policy", custom, http.MethodGet, "/app.js", http.StatusOK, ownCSP, ownReferrer},
		{"a 404, own policy", custom, http.MethodGet, "/missing", http.StatusNotFound, ownCSP, ownReferrer},
		{"a 500, own policy", custom, http.MethodGet, "/broken.js", http.StatusInternalServerError, ownCSP, ownReferrer},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		response := get(c.handler, c.method, c.target)
		header := response.Header()
		//: the answer expected.
		if response.Code != c.wantStatus {
			t.Fatalf("%s %s = %d, want %d", c.method, c.target, response.Code, c.wantStatus)
		}
		//: the policy.
		if got := header.Get("Content-Security-Policy"); got != c.wantCSP {
			t.Errorf("Content-Security-Policy = %q, want %q", got, c.wantCSP)
		}
		//: no sniffing, ever.
		if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		//: the referrer policy.
		if got := header.Get("Referrer-Policy"); got != c.wantReferrer {
			t.Errorf("Referrer-Policy = %q, want %q", got, c.wantReferrer)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestOnlyGetAndHeadAreAnswered pins the 405: any other method is refused
// before the name is looked up, and says what is allowed.
func TestOnlyGetAndHeadAreAnswered(t *testing.T) {
	t.Parallel()
	handler := build(t, site(), static.Config{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		response := get(handler, method, "/app.js")
		//: refused, with the methods that are not.
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /app.js = %d, Allow %q; want 405 and GET, HEAD", method, response.Code, response.Header().Get("Allow"))
		}
	}
}

// TestContentHashedNamesAreCachedForGood pins the caching split: a file the
// predicate marks is immutable for a year, everything else — the shell, a
// fallback, a 404 under the same prefix — is revalidated; and the predicate
// is asked about the cleaned name of the file actually served.
func TestContentHashedNamesAreCachedForGood(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var asked []string
	handler := build(t, site(), static.Config{
		SinglePageApp: true,
		Immutable: func(name string) bool {
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			return strings.HasPrefix(name, "assets/")
		},
	})
	type tc struct {
		target string
		want   string
	}
	tests := []tc{
		{"/assets/app-3f2a9c.js", static.ImmutableCacheControl},
		{"//assets/./style-77e0b1.css", static.ImmutableCacheControl},
		{"/index.html", static.RevalidateCacheControl},
		{"/settings", static.RevalidateCacheControl},
		{"/assets/app-000000.js", static.RevalidateCacheControl},
		{"/guide", static.RevalidateCacheControl},
	}
	//: sequential: the predicate's record is read afterwards.
	for _, c := range tests {
		response := get(handler, http.MethodGet, c.target)
		//: the Cache-Control of that answer.
		if got := response.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.target, got, c.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"assets/app-3f2a9c.js", "assets/style-77e0b1.css", "index.html", "index.html"}
	//: the names of the files served, cleaned, and a fallback asked as its shell.
	if !slices.Equal(asked, want) {
		t.Errorf("the predicate was asked %q, want %q", asked, want)
	}
}

// TestNewRefusesWhatItCannotServeSafely pins STATIC_MISCONFIGURED: each
// refusal names its option, and a valid list of Referrer-Policy tokens is
// accepted as the fallback chain it is.
func TestNewRefusesWhatItCannotServeSafely(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		fsys   fs.FS
		cfg    static.Config
		option string
	}
	tests := []tc{
		{"no tree", nil, static.Config{}, "fs"},
		{"a newline in the policy", site(), static.Config{ContentSecurityPolicy: "default-src 'self'\r\nX-Injected: 1"}, "content_security_policy"},
		{"a NUL in the policy", site(), static.Config{ContentSecurityPolicy: "default-src 'self'\x00"}, "content_security_policy"},
		{"a DEL in the policy", site(), static.Config{ContentSecurityPolicy: "default-src\x7f"}, "content_security_policy"},
		{"a token in the wrong case", site(), static.Config{ReferrerPolicy: "Same-Origin"}, "referrer_policy"},
		{"an unknown token", site(), static.Config{ReferrerPolicy: "same-site"}, "referrer_policy"},
		{"an empty item", site(), static.Config{ReferrerPolicy: "same-origin,"}, "referrer_policy"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		handler, err := static.NewHandler(c.fsys, c.cfg)
		//: refused, with no handler.
		if handler != nil || !errors.Is(err, corenet.StaticMisconfigured) {
			t.Fatalf("New() = %v, %v; want nil and STATIC_MISCONFIGURED", handler, err)
		}
		//: naming the option, never its value.
		if got := optionOf(err); got != c.option {
			t.Errorf("option field = %q, want %q", got, c.option)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a fallback chain of known tokens, spaced as headers are.
	if _, err := static.NewHandler(site(), static.Config{ReferrerPolicy: "no-referrer, strict-origin-when-cross-origin"}); err != nil {
		t.Errorf("a list of known tokens was refused: %v", err)
	}
	//: a tab is the one control character a header value may hold.
	if _, err := static.NewHandler(site(), static.Config{ContentSecurityPolicy: "default-src\t'self'"}); err != nil {
		t.Errorf("a policy with a tab was refused: %v", err)
	}
}

// optionOf returns the option field a refusal carries.
func optionOf(err error) string {
	//: the one field this refusal writes.
	for _, field := range errs.FieldsOf(err) {
		//: found.
		if field.Key() == "option" {
			return field.StringValue()
		}
	}
	return ""
}

// TestTheZeroHandlerAnswers500WithTheDefaults pins the zero value: a handler
// built without New has no tree, which is the server's fault — a 500 — and
// still sends the default headers.
func TestTheZeroHandlerAnswers500WithTheDefaults(t *testing.T) {
	t.Parallel()
	response := get(&static.Handler{}, http.MethodGet, "/")
	//: the server's fault.
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("GET / = %d, want 500", response.Code)
	}
	//: the defaults, never an empty header.
	if got := response.Header().Get("Content-Security-Policy"); got != static.DefaultContentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want the default", got)
	}
	//: the defaults, never an empty header.
	if got := response.Header().Get("Referrer-Policy"); got != static.DefaultReferrerPolicy {
		t.Errorf("Referrer-Policy = %q, want the default", got)
	}
}
