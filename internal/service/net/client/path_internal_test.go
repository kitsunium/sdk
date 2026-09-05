// Package client — the path safety checks that run before any allowlist.
//
// These exist because an anchored pattern is not sufficient on its own, and
// nothing else in the Go stack will catch the gap: url.URL does not reduce dot
// segments and neither does the transport, so a path that means one thing to the
// policy and another to the upstream reaches the wire unchanged.
package client

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_checkPath pins the three refusals, and what each one prevents.
func Test_checkPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
		//: the "why" annotation the refusal must carry, or "" when admitted.
		wantWhy string
	}
	tests := []tc{
		{name: "a plain absolute path", path: "/v1/subscribers"},
		{name: "a path with an identifier", path: "/v1/supi/001010123456789"},
		{name: "a path with a query-looking segment", path: "/v1/supi/a=b"},
		{name: "the root", path: "/"},
		//: a scheme-relative reference IS an absolute path as far as this
		//: check is concerned, and it passes. Whether the resolved HOST still
		//: matches the configured base is a question this function does not
		//: ask — see the note in the package review.
		{name: "a scheme-relative reference", path: "//example.test/v1"},
		//: relative: it would resolve against whatever base the transport
		//: happens to hold, escaping the reviewed surface entirely.
		{name: "a relative path", path: "v1/subscribers", wantWhy: "path is not absolute"},
		{name: "an empty path", path: "", wantWhy: "path is not absolute"},
		//: dot segments: `^/v1/supi/[^/]+$` matches `/v1/supi/..` perfectly
		//: well, and the upstream normalises it to a different resource.
		{name: "a parent segment", path: "/v1/supi/..", wantWhy: "path carries a dot segment"},
		{name: "a current segment", path: "/v1/./supi", wantWhy: "path carries a dot segment"},
		{name: "an encoded parent segment", path: "/v1/supi/%2e%2e", wantWhy: "path carries a dot segment"},
		{name: "an uppercase encoded parent", path: "/v1/supi/%2E%2E", wantWhy: "path carries a dot segment"},
		{name: "a half-encoded parent", path: "/v1/supi/.%2e", wantWhy: "path carries a dot segment"},
		//: an encoded separator makes the wire form and the upstream's view
		//: disagree, so no pattern can authorise it honestly.
		{name: "an encoded slash", path: "/v1/supi/a%2fb", wantWhy: "path carries a percent-encoded separator"},
		{name: "an uppercase encoded slash", path: "/v1/supi/a%2Fb", wantWhy: "path carries a percent-encoded separator"},
		{name: "an encoded backslash", path: "/v1/supi/a%5cb", wantWhy: "path carries a percent-encoded separator"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := checkPath(c.path)

		if c.wantWhy == "" {
			if err != nil {
				t.Fatalf("checkPath(%q) = %v, want nil", c.path, err)
			}
			return
		}
		if !errs.HasCode(err, corenet.CodeUnsafePath) {
			t.Fatalf("checkPath(%q) = %v, want UNSAFE_PATH", c.path, err)
		}
		//: the refusal says WHY without echoing the path — a denial that
		//: quoted it would leak the private API surface into a log line.
		var why string
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "why" {
				why = f.StringValue()
			}
			if f.StringValue() == c.path && c.path != "" {
				t.Errorf("the refusal echoed the path: %v", errs.FieldsOf(err))
			}
		}
		if why != c.wantWhy {
			t.Errorf("checkPath(%q) said %q, want %q", c.path, why, c.wantWhy)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hasDotSegment pins every spelling a peer would re-normalise. The list is
// not decorative: %2e and . are the same octet to any RFC 3986 implementation,
// so a check that only saw the literal form would be trivially bypassed.
func Test_hasDotSegment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
		want bool
	}
	tests := []tc{
		{"a plain path", "/v1/subscribers", false},
		{"a segment merely containing a dot", "/v1/a.b", false},
		{"a segment starting with a dot", "/v1/.hidden", false},
		{"a trailing empty segment", "/v1/", false},
		{"a current segment", "/v1/./x", true},
		{"a parent segment", "/v1/../x", true},
		{"a trailing parent segment", "/v1/..", true},
		{"an encoded current segment", "/v1/%2e/x", true},
		{"an encoded parent segment", "/v1/%2e%2e/x", true},
		{"an uppercase encoded parent", "/v1/%2E%2E/x", true},
		{"a dot then an encoded dot", "/v1/.%2e/x", true},
		{"an encoded dot then a dot", "/v1/%2e./x", true},
		{"a parent as the only segment", "..", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := hasDotSegment(c.path); got != c.want {
			t.Errorf("hasDotSegment(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hasEncodedSeparator pins the check that closes the gap judging the
// escaped path opens.
//
// Patterns match the WIRE form, so `/v1/supi/a%2fb` satisfies `^/v1/supi/[^/]+$`
// as a single segment — while an upstream that decodes before routing sees
// `/v1/supi/a/b`: two segments, a different resource than the policy believed it
// was authorising. The two views disagree, and the only honest answer is to
// refuse.
func Test_hasEncodedSeparator(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		path string
		want bool
	}
	tests := []tc{
		{"a plain path", "/v1/supi/abc", false},
		{"a literal slash", "/v1/supi/a/b", false},
		{"another percent escape", "/v1/supi/a%20b", false},
		{"an encoded slash", "/v1/supi/a%2fb", true},
		{"an uppercase encoded slash", "/v1/supi/a%2Fb", true},
		{"a mixed-case encoded slash", "/v1/supi/a%2Fb", true},
		{"an encoded backslash", "/v1/supi/a%5cb", true},
		{"an uppercase encoded backslash", "/v1/supi/a%5Cb", true},
		{"an encoded slash anywhere in the path", "/a%2fb/v1", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := hasEncodedSeparator(c.path); got != c.want {
			t.Errorf("hasEncodedSeparator(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
