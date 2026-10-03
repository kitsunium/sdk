package redact_test

import (
	"maps"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/security/redact"
)

// signIn is a request a trace would show.
type signIn struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Recovery string `json:"recovery" app:"secret"`
}

// TestTheFacadeRedactsAValueATextAndARecord walks the public surface the way
// a framework's trace and log panels use it, with nothing internal imported.
func TestTheFacadeRedactsAValueATextAndARecord(t *testing.T) {
	t.Parallel()
	r := redact.New(redact.Config{Tag: "app"})
	shown, err := r.Value(signIn{Email: "ann@example.com", Password: "hunter2", Recovery: "blue-horse"}, 1024)
	if err != nil || shown.Truncated {
		t.Fatalf("Value() = %v, truncated %v", err, shown.Truncated)
	}
	if strings.Contains(string(shown.JSON), "hunter2") || strings.Contains(string(shown.JSON), "blue-horse") {
		t.Errorf("a secret was shown: %s", shown.JSON)
	}
	if got := r.Text("retrying https://svc:hunter2@queue.local", 64); strings.Contains(got, "hunter2") {
		t.Errorf("Text() showed credentials: %q", got)
	}
	pairs := maps.Collect(r.Attrs([]logger.Attr{logger.String("api_key", "k-1"), logger.Int("n", 2)}, 64))
	if pairs["api_key"] != redact.Placeholder || pairs["n"] != "2" {
		t.Errorf("Attrs() = %v", pairs)
	}
	if _, err := r.JSON([]byte(`{"a":`), 64); !errs.HasCode(err, redact.CodeDocumentInvalid) {
		t.Errorf("JSON(malformed) = %v, want DOCUMENT_INVALID", err)
	}
	if words := redact.DefaultWords(); len(words) == 0 || !r.Name("Set-Cookie") {
		t.Error("the default words are not in force")
	}
}

// fixedRedactor is the double a consumer writes: it embeds the port and
// answers the one method the code under test calls, with no engine behind it.
type fixedRedactor struct{ redact.Redactor }

// Text shows every text as the placeholder.
func (fixedRedactor) Text(string, int) string {
	//: whatever was asked, nothing of it is shown.
	return redact.Placeholder
}

// TestADoubleStandsWhereARedactorIsExpected pins what the port is for (ADR
// 0160): code that accepts a Redactor takes New's engine and a consumer's own
// double alike, through the facade alone.
func TestADoubleStandsWhereARedactorIsExpected(t *testing.T) {
	t.Parallel()
	shown := func(r redact.Redactor) string { return r.Text("https://svc:hunter2@queue.local", 64) }
	for _, tc := range []struct {
		name     string
		redactor redact.Redactor
	}{
		{"the SDK's engine", redact.New(redact.Config{})},
		{"a consumer's double", fixedRedactor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shown(tc.redactor); strings.Contains(got, "hunter2") || !strings.Contains(got, redact.Placeholder) {
				t.Errorf("Text() through the port = %q", got)
			}
		})
	}
}
