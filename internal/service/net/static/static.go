// Package static serves a tree of files from an io/fs.FS over HTTP — what
// http.FileServerFS does, and what it does not (ADR 0130).
//
// A request path is cleaned from the root before anything is looked up, so a
// ".." never climbs above the tree, whatever the file system does with names.
// A directory is never listed: it is served by its index.html, or it is 404.
// An optional single-page-application fallback serves the root's index.html
// for a path with no extension that names nothing, so client-side routes
// survive a reload while a missing script is still a 404 and never a page of
// HTML. Every response — a file, a redirect, a 404, a 405, a 500 — carries a
// Content-Security-Policy, X-Content-Type-Options: nosniff and a
// Referrer-Policy. A file the caller marks content-hashed is cached for a year
// and never revalidated; everything else is revalidated before each use.
//
// Only GET and HEAD are answered, and HEAD answers exactly what GET would,
// without the body. Every status goes through the ResponseWriter the handler
// was given, so a wrapping handler that records the status sees every 5xx: a
// file the tree holds but cannot open, stat or seek.
package static

import (
	"cmp"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// DefaultContentSecurityPolicy is the policy sent when Config names none: the
// page's own origin for everything it loads, no base URL or form target
// elsewhere, and no framing by another page.
const DefaultContentSecurityPolicy string = "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// DefaultReferrerPolicy is the Referrer-Policy sent when Config names none: a
// full referrer to this origin, none at all to another.
const DefaultReferrerPolicy string = "same-origin"

// ImmutableCacheControl is the Cache-Control of a file Config.Immutable marks
// content-hashed: stored for a year, shared caches included, and never
// revalidated — a new version has a new name.
const ImmutableCacheControl string = "public, max-age=31536000, immutable"

// RevalidateCacheControl is the Cache-Control of every other response: stored,
// but revalidated before each use, so a redeployed page is never served stale.
const RevalidateCacheControl string = "no-cache"

// indexName is the file a directory is served by, and the single-page
// application's shell.
const indexName string = "index.html"

// allowedMethods is the Allow header of a 405: the two methods answered.
const allowedMethods string = "GET, HEAD"

// Config says how a tree is served. Its zero value is a working, strict
// configuration: the default policies, no fallback, nothing cached for good.
type Config struct {
	// ContentSecurityPolicy is sent on every response. Empty sends
	// DefaultContentSecurityPolicy; there is no way to send none. A value
	// holding a control character is refused at construction.
	ContentSecurityPolicy string
	// ReferrerPolicy is sent on every response: one of the eight tokens the
	// Referrer Policy specification defines, or a comma-separated list of
	// them. Empty sends DefaultReferrerPolicy. Any other value is refused at
	// construction, because a browser ignores a token it does not know and falls back
	// to its own default, which sends the origin to every site.
	ReferrerPolicy string
	// SinglePageApp serves the root's index.html, with 200, for a path that
	// has no extension and names nothing in the tree. A path with an
	// extension that names nothing stays a 404, and so does a directory with
	// no index.html.
	SinglePageApp bool
	// Immutable reports whether the file at name — slash-separated, relative
	// to the root, as served: "assets/app-3f2a9c.js" — is content-hashed, so
	// its response may be cached with ImmutableCacheControl. For a fallback it
	// is asked about "index.html", the file actually served. Nil marks
	// nothing.
	Immutable func(name string) bool
}

// Handler serves one file tree. It is safe for concurrent use.
//
// The zero Handler has no tree and answers every request 500, with the
// default headers. Build one with NewHandler.
type Handler struct {
	fsys      fs.FS
	csp       string
	referrer  string
	spa       bool
	immutable func(name string) bool
}

// NewHandler returns a Handler serving fsys as cfg says. It refuses, with the core
// net sentinel STATIC_MISCONFIGURED and an "option" field naming what, a nil
// fsys, a header value holding a control character, and a Referrer-Policy that
// is not a list of known tokens. It reads nothing from fsys: a tree that
// appears after the handler is built is served once it is there.
//
// To serve a sub-directory — the "dist" of an embed.FS — hand NewHandler the result
// of fs.Sub. The handler confines request NAMES to the tree it is given; what
// the tree's own entries point at is the file system's business, so serve an
// os.Root's FS rather than os.DirFS where a link could lead out of it.
func NewHandler(fsys fs.FS, cfg Config) (*Handler, error) {
	//: nothing to serve.
	if fsys == nil {
		//: STATIC_MISCONFIGURED, naming the option.
		return nil, misconfigured("fs")
	}
	csp := cmp.Or(cfg.ContentSecurityPolicy, DefaultContentSecurityPolicy)
	//: a value net/http would rewrite, or a browser reject, is refused here.
	if !headerValueSafe(csp) {
		//: STATIC_MISCONFIGURED, naming the option.
		return nil, misconfigured("content_security_policy")
	}
	referrer := cmp.Or(cfg.ReferrerPolicy, DefaultReferrerPolicy)
	//: a token no browser knows is a policy no browser applies.
	if !referrerPolicyKnown(referrer) {
		//: STATIC_MISCONFIGURED, naming the option.
		return nil, misconfigured("referrer_policy")
	}
	//: ready to serve.
	return &Handler{fsys: fsys, csp: csp, referrer: referrer, spa: cfg.SinglePageApp, immutable: cfg.Immutable}, nil
}

// ServeHTTP answers one request: the security headers first, then the method,
// then the name.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", cmp.Or(h.csp, DefaultContentSecurityPolicy))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", cmp.Or(h.referrer, DefaultReferrerPolicy))
	header.Set("Cache-Control", RevalidateCacheControl)
	//: only reading is answered, and a refusal says what is.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", allowedMethods)
		//: 405, before the name is looked at.
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	//: the zero Handler: a server built wrong, which is the server's fault.
	if h.fsys == nil {
		//: 500.
		failed(w)
		return
	}
	//: the cleaned name decides, and a trailing slash says a directory was asked for.
	h.serveName(w, r, cleanName(r.URL.Path), strings.HasSuffix(r.URL.Path, "/"))
}

// cleanName turns a request path into a name of the tree: cleaned from the
// root, so no ".." survives and nothing can climb above it, then made
// relative as io/fs requires. The root is ".".
func cleanName(requestPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	//: the root itself.
	if name == "" {
		//: io/fs's name for it.
		return "."
	}
	//: a valid io/fs name: no dot element, no empty element, no leading slash.
	return name
}

// serveName serves what name is: a file, a directory, or nothing.
func (h *Handler) serveName(w http.ResponseWriter, r *http.Request, name string, slash bool) {
	file, info, found := open(h.fsys, name)
	//: what the name is decides how it is answered.
	switch {
	//: a directory, served by its index.html or not at all.
	case found == present && info.IsDir():
		closeQuietly(file)
		h.serveDirectory(w, r, name, slash)
	//: a file, asked for as one.
	case found == present && info.Mode().IsRegular() && !slash:
		defer closeQuietly(file)
		h.serveFile(w, r, name, file, info)
	//: a file asked for as a directory, or a device, a pipe, a socket.
	case found == present:
		closeQuietly(file)
		notFound(w)
	//: nothing by that name.
	case found == absent:
		h.fallback(w, r, name)
	//: something the tree holds and could not open.
	default:
		failed(w)
	}
}

// serveDirectory serves a directory by its index.html: redirecting first to
// the name with a trailing slash, so the page's relative links resolve inside
// the directory. A directory without one is 404 — never a listing, and never
// the single-page application's shell.
func (h *Handler) serveDirectory(w http.ResponseWriter, r *http.Request, name string, slash bool) {
	index := path.Join(name, indexName)
	file, info, found := open(h.fsys, index)
	//: what the index is decides how the directory is answered.
	switch {
	//: the index, served as the directory.
	case found == present && info.Mode().IsRegular():
		defer closeQuietly(file)
		//: "docs" becomes "docs/" before its page is served; the root cannot.
		if !slash && name != "." {
			redirectToDirectory(w, r, name)
			return
		}
		h.serveFile(w, r, index, file, info)
	//: an "index.html" that is itself a directory, or not a file.
	case found == present:
		closeQuietly(file)
		notFound(w)
	//: no index: no listing.
	case found == absent:
		notFound(w)
	//: an index the tree holds and could not open.
	default:
		failed(w)
	}
}

// fallback answers a name that names nothing: the root's index.html for a
// single-page application's route, 404 for everything else.
func (h *Handler) fallback(w http.ResponseWriter, r *http.Request, name string) {
	//: a missing asset — anything with an extension — is a 404, never HTML.
	if !h.spa || path.Ext(name) != "" {
		//: 404.
		notFound(w)
		return
	}
	file, info, found := open(h.fsys, indexName)
	//: what the shell is decides how the route is answered.
	switch {
	//: the application's shell, for its client-side router.
	case found == present && info.Mode().IsRegular():
		defer closeQuietly(file)
		h.serveFile(w, r, indexName, file, info)
	//: no shell to serve.
	case found == present:
		closeQuietly(file)
		notFound(w)
	//: no shell to serve.
	case found == absent:
		notFound(w)
	//: a shell the tree holds and could not open.
	default:
		failed(w)
	}
}

// redirectToDirectory sends a directory named without its trailing slash to
// the name with one. The Location is relative, so it stays right behind
// http.StripPrefix, and starts with "./", so no directory name can make it
// read as another host or a scheme. The name is escaped as one path segment:
// a directory called "v2?beta" or "a#b" is redirected to itself, not to a
// query or a fragment of another path.
func redirectToDirectory(w http.ResponseWriter, r *http.Request, name string) {
	location := "./" + url.PathEscape(path.Base(name)) + "/"
	//: the query travels with the redirect.
	if r.URL.RawQuery != "" {
		location += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusMovedPermanently)
}

// notFound answers 404.
func notFound(w http.ResponseWriter) {
	//: the status and its text, nothing about the tree.
	http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
}

// failed answers 500: the tree holds something it could not serve.
func failed(w http.ResponseWriter) {
	//: the status and its text, nothing about the cause.
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// closeQuietly closes a file the handler is done with. A close failure on a
// read-only file changes nothing the client can be told.
func closeQuietly(file io.Closer) {
	//: nothing held past this point either way.
	if err := file.Close(); err != nil {
		return
	}
}
