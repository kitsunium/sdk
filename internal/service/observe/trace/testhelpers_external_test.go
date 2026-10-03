package trace_test

import (
	"io"
	"net/http"
	"testing"
)

// readAll drains a request body, returning nil on a fault. A fault here fails
// the assertion that compares the bytes, which is a clearer message than a
// second error path.
func readAll(r *http.Request) []byte {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	return body
}

// writeString writes text to w from inside a test server's handler, reporting a
// write fault on t rather than discarding it. It uses Errorf, not Fatalf: it runs
// on the server's goroutine, where Fatalf would not stop the test.
func writeString(t *testing.T, w io.Writer, text string) {
	t.Helper()
	if text == "" {
		return
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Errorf("writing the test response body: %v", err)
	}
}

// closeBody closes a response body and reports a close fault, which is the one
// the SDK's own client exists to stop callers from forgetting.
func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if response == nil || response.Body == nil {
		return
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("closing the response body: %v", err)
	}
}
