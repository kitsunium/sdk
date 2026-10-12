package kit

import (
	"context"
	"strings"
	"testing"
)

// lazyBodyCLI is the command TestABodyIsResolvedWhenRead declares.
func lazyBodyCLI(context.Context, []string, StdioValue) int { return 0 }

// A declaration records where its function's body starts, and resolves it —
// runtime.FuncForPC, the function's file and line — when something reads it,
// not when the product declares it: a start that draws nothing pays none of
// it, once per declaration of every role a binary holds (#276).
func TestABodyIsResolvedWhenRead(t *testing.T) {
	svc := NewService("lazy-body", "A product.")
	c := svc.CLI("run", "Runs.", lazyBodyCLI)
	if c.body == nil || c.body.s == nil {
		t.Fatal("the declaration recorded no body")
	}
	if c.body.s.file != "" || c.body.s.line != 0 {
		t.Fatalf("the body was resolved at the declaration: %s:%d", c.body.s.file, c.body.s.line)
	}
	if !strings.HasSuffix(c.body.file(), "body_internal_test.go") || c.body.line() == 0 || !strings.HasSuffix(c.body.fn(), ".lazyBodyCLI") {
		t.Errorf("the body reads %s:%d %s", c.body.file(), c.body.line(), c.body.fn())
	}
	if nilBody := svc.CLI("none", "Nothing.", nil); nilBody.body != nil {
		t.Errorf("a nil function has a body: %+v", nilBody.body)
	}
}
