//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/static .

// Package static serves a tree of files — a single-page application, a
// documentation site, an embed.FS — over HTTP: what http.FileServerFS does,
// and what it does not (ADR 0130).
//
// # The whole thing
//
//	//go:embed dist
//	var dist embed.FS
//
//	tree, _ := fs.Sub(dist, "dist")
//	assets, err := static.New(tree, static.Config{
//		SinglePageApp: true,
//		Immutable:     func(name string) bool { return strings.HasPrefix(name, "assets/") },
//	})
//	if err != nil {
//		return err
//	}
//	mux.Handle("GET /app/", http.StripPrefix("/app", assets))
//
// # What it refuses
//
//   - A request path is cleaned from the root before anything is looked up,
//     so no spelling of ".." climbs above the tree — whatever the file
//     system itself does with a name.
//   - A directory is never listed. It is served by its index.html, after a
//     redirect to its name with a trailing slash so the page's relative links
//     resolve inside it; a directory with no index.html is a 404.
//   - Only GET and HEAD are answered; anything else is a 405 with Allow. HEAD
//     answers exactly what GET would, without the body.
//
// # A single-page application
//
// With [Config].SinglePageApp, a path that has no extension and names nothing
// is served the root's index.html with a 200, so a client-side route
// survives a reload. A path WITH an extension that names nothing stays a 404:
// a script tag must never receive a page of HTML in place of the script it
// asked for.
//
// # Headers
//
// Every response — a file, a redirect, a 404, a 405, a 500 — carries a
// Content-Security-Policy ([DefaultContentSecurityPolicy] unless
// [Config].ContentSecurityPolicy names one), X-Content-Type-Options: nosniff,
// and a Referrer-Policy ([DefaultReferrerPolicy], "same-origin", unless
// [Config].ReferrerPolicy names one). [New] refuses a policy holding a control
// character, and a Referrer-Policy that is not a list of the tokens the
// specification defines, since a browser ignores one it does not know.
//
// Under nosniff a browser refuses a script or a stylesheet served with the
// wrong type, and the standard library lets a host's MIME tables override its
// own. The types a page cannot run without — HTML, JavaScript, CSS, JSON,
// WebAssembly, SVG, a web manifest — are therefore pinned, the same on every
// host.
//
// A file [Config].Immutable marks content-hashed is sent with
// [ImmutableCacheControl]: cached for a year and never revalidated. Every
// other response is sent with [RevalidateCacheControl], so a redeployed page
// is never served stale.
//
// # Errors a wrapper can see
//
// Every status goes through the ResponseWriter the handler was given, so a
// handler wrapped by one that records the status sees every 5xx. A name the
// file system refuses — nothing there, a character it cannot hold, a file
// used as a directory, a component too long — is the client's 404, because a
// client chooses every name it asks for and could otherwise produce a 5xx at
// will. A name the tree holds and cannot open, stat or seek — a permission
// refused, a storage failure — is a 500. A failure after the status is sent
// cannot change it; the body is cut short of its declared length instead.
package static
