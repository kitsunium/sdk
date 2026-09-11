package view_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

// failingFS is an fs.FS that refuses to describe itself, which is what a bad
// embed pattern or a missing directory in a container looks like.
type failingFS struct{}

// Open always fails, so fs.WalkDir reports a source error at the root.
func (failingFS) Open(_ string) (fs.File, error) {
	return nil, fs.ErrPermission
}

// TestARenderThatFailsMidwayReturnsNothing is the all-or-nothing claim, and
// the entire reason Render returns a slice rather than taking an io.Writer.
//
// Handed an http.ResponseWriter, the same failure would have flushed the
// status line, the headers and a plausible-looking prefix of the page before
// it was detectable — at which point 500 is no longer sendable.
func TestARenderThatFailsMidwayReturnsNothing(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `OK-PREFIX{{.User.Name}}`)})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "p.html", struct{ User any }{User: nil})
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, coreview.RenderFailed) {
		t.Fatalf("err = %v, want RenderFailed", renderErr)
	}
}

// TestARenderPastTheCeilingIsStoppedAndDiscarded.
//
// A {{range}} over an attacker-influenced collection has no recursion depth,
// so the stdlib's 100000-frame limit never fires; the byte cap is the only
// thing between that template and the machine's memory.
func TestARenderPastTheCeilingIsStoppedAndDiscarded(t *testing.T) {
	const ceiling int = 64
	renderer, err := svcview.NewHTML(coreview.Config{
		FS:       page("p.html", `{{range .}}0123456789{{end}}`),
		MaxBytes: ceiling,
	})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "p.html", make([]int, 1000))
	if document != nil {
		t.Fatalf("document = %d bytes, want nil", len(document))
	}
	if !errors.Is(renderErr, coreview.RenderTooLarge) {
		t.Fatalf("err = %v, want RenderTooLarge", renderErr)
	}
	if got := fieldMap(errs.FieldsOf(renderErr))["limit"]; got != "64" {
		t.Fatalf("limit field = %q, want 64", got)
	}
}

// TestARenderExactlyAtTheCeilingSucceeds pins that the bound is inclusive —
// the engine stops AT the ceiling, not one byte before it.
func TestARenderExactlyAtTheCeilingSucceeds(t *testing.T) {
	const body string = "0123456789"
	renderer, err := svcview.NewHTML(coreview.Config{
		FS:       page("p.html", body),
		MaxBytes: len(body),
	})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "p.html", nil)
	if renderErr != nil {
		t.Fatalf("a render of exactly MaxBytes was refused: %v", renderErr)
	}
	if string(document) != body {
		t.Fatalf("document = %q", document)
	}
}

// TestANonPositiveMaxBytesClampsRatherThanRefuses is ADR 0031's clamp half.
//
// Unlike a lease TTL, whose two readings are opposites, there IS a defensible
// universal answer for "an HTML document should not exceed N" — so refusing
// view.Config{FS: templates} would make the obvious spelling unusable for no
// safety gain.
func TestANonPositiveMaxBytesClampsRatherThanRefuses(t *testing.T) {
	for _, configured := range []int{0, -1} {
		renderer, err := svcview.NewHTML(coreview.Config{
			FS:       page("p.html", `{{range .}}0123456789{{end}}`),
			MaxBytes: configured,
		})
		if err != nil {
			t.Fatalf("MaxBytes=%d was refused: %v", configured, err)
		}
		// 100 000 bytes is far past any accidental zero ceiling and far under
		// the 8 MiB default it must have clamped to.
		document, renderErr := renderer.Render(t.Context(), "p.html", make([]int, 10_000))
		if renderErr != nil {
			t.Fatalf("MaxBytes=%d rendered 100 kB with error %v", configured, renderErr)
		}
		if len(document) != 100_000 {
			t.Fatalf("document = %d bytes, want 100000", len(document))
		}
	}
}

// TestAnAlreadyCancelledContextBuysNoWork, and returns the caller's own
// verdict rather than an SDK sentinel — the caller supplied the deadline and
// already knows what it means.
func TestAnAlreadyCancelledContextBuysNoWork(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", "BODY")})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	document, renderErr := renderer.Render(ctx, "p.html", nil)
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", renderErr)
	}
	if _, isSDK := errs.CodeOf(renderErr); isSDK {
		t.Fatal("a context cancellation was relabelled as an SDK sentinel")
	}
}

// TestConcurrentRendersDoNotShareABuffer. The scratch is pooled, so this is
// the test that fails if a buffer is ever handed out without being copied.
func TestConcurrentRendersDoNotShareABuffer(t *testing.T) {
	files := fstest.MapFS{"p.html": &fstest.MapFile{Data: []byte(`<p>{{.}}</p>`)}}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	const workers int = 32
	documents := make([]string, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for worker := range workers {
		go func() {
			defer group.Done()
			document, renderErr := renderer.Render(t.Context(), "p.html", worker)
			if renderErr != nil {
				documents[worker] = "ERR"
				return
			}
			documents[worker] = string(document)
		}()
	}
	group.Wait()
	for worker := range workers {
		want := "<p>" + itoa(worker) + "</p>"
		if documents[worker] != want {
			t.Fatalf("worker %d rendered %q, want %q", worker, documents[worker], want)
		}
	}
}

// TestTheReturnedDocumentIsNotAliasedByTheNextRender: the copy-out is what
// makes a pooled buffer safe.
func TestTheReturnedDocumentIsNotAliasedByTheNextRender(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `<p>{{.}}</p>`)})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	first, firstErr := renderer.Render(t.Context(), "p.html", "AAAA")
	if firstErr != nil {
		t.Fatalf("first render: %v", firstErr)
	}
	if _, secondErr := renderer.Render(t.Context(), "p.html", "BBBB"); secondErr != nil {
		t.Fatalf("second render: %v", secondErr)
	}
	if !strings.Contains(string(first), "AAAA") {
		t.Fatalf("the first document was overwritten by the second: %q", first)
	}
}

// itoa is strconv.Itoa under a local name, so the concurrency test's expected
// value is built without pulling a formatter into the assertion.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
