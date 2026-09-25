package strictjson

import (
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_boundPointer pins the cut: at a token separator, never inside a token,
// so what is kept still names a real ancestor.
func Test_boundPointer(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 200)
	type tc struct {
		name    string
		pointer string
		want    string
	}
	tests := []tc{
		{"a short pointer is unchanged", "/items/1/price", "/items/1/price"},
		{"the root is unchanged", "", ""},
		{"a long pointer is cut at the last separator that fits", "/" + long + "/" + long, "/" + long},
		{"a single token past the bound is the root", "/" + strings.Repeat("b", 300), ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := boundPointer(c.pointer); got != c.want {
			t.Errorf("boundPointer() = %d bytes %q…, want %d bytes", len(got), got[:min(len(got), 16)], len(c.want))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_boundedReader_verdict pins the order of the reading's own findings:
// size first, then a failed source, then an empty one.
func Test_boundedReader_verdict(t *testing.T) {
	t.Parallel()
	failure := errors.New("connection reset")
	type tc struct {
		name      string
		reader    boundedReader
		oversize  func(error) bool
		wantCode  errs.Code
		wantClean bool
	}
	tests := []tc{
		{name: "within the bound", reader: boundedReader{delivered: 10}, wantClean: true},
		{name: "past the bound", reader: boundedReader{delivered: 65}, wantCode: CodeDocumentTooLarge},
		{name: "past the bound beats a failure", reader: boundedReader{delivered: 65, failure: failure}, wantCode: CodeDocumentTooLarge},
		{name: "a failure", reader: boundedReader{delivered: 3, failure: failure}, wantCode: CodeDocumentUnreadable},
		{
			name: "a failure that is a size refusal", reader: boundedReader{delivered: 64, failure: failure},
			oversize: func(error) bool { return true }, wantCode: CodeDocumentTooLarge,
		},
		{name: "nothing at all", reader: boundedReader{}, wantCode: CodeDocumentEmpty},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := c.reader.verdict(64, c.oversize)
		if c.wantClean {
			if err != nil {
				t.Fatalf("verdict() = %v, want nil", err)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("verdict() = %v, want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
