package trace_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// TestOTLPHTTPExportDocumentPostsTheStoredBytes pins the sibling a
// store-and-forward queue posts through (ADR 0185): the exporter
// NewOTLPHTTPExporter returns is a DocumentExporter, the document reaches the
// collector byte for byte, an empty one posts nothing, and a refusal carries
// the verdict ExportContext gives — the code, the retryable class and the
// collector's Retry-After.
func TestOTLPHTTPExportDocumentPostsTheStoredBytes(t *testing.T) {
	status := http.StatusOK
	calls := 0
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotBody = readAll(r)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	document, ok := exporter.(coretrace.DocumentExporter)
	if !ok {
		t.Fatal("the OTLP/HTTP exporter is not a DocumentExporter")
	}
	doc, err := svctrace.EncodeOTLPJSON(fixtureSpans(t))
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if exportErr := document.ExportDocument(context.Background(), doc); exportErr != nil {
		t.Fatalf("ExportDocument: %v", exportErr)
	}
	if string(gotBody) != string(doc) {
		t.Errorf("body = %s, want the stored document exactly", gotBody)
	}
	if exportErr := document.ExportDocument(context.Background(), nil); exportErr != nil || calls != 1 {
		t.Errorf("an empty document = %v after %d request(s), want nil after 1", exportErr, calls)
	}
	status = http.StatusTooManyRequests
	exportErr := document.ExportDocument(context.Background(), doc)
	if !errors.Is(exportErr, coretrace.OTLPExportUnavailable) || !svctrace.OTLPRetryable(exportErr) {
		t.Fatalf("a 429 = %v, want the retryable OTLPExportUnavailable", exportErr)
	}
	if got := fieldValue(exportErr, "retry_after_seconds"); got != "7" {
		t.Errorf("retry_after_seconds = %q, want the collector's 7", got)
	}
	status = http.StatusBadRequest
	if exportErr := document.ExportDocument(context.Background(), doc); !errors.Is(exportErr, coretrace.OTLPExportRejected) || svctrace.OTLPRetryable(exportErr) {
		t.Errorf("a 400 = %v, want the permanent OTLPExportRejected", exportErr)
	}
}

// fieldValue renders the value of the outermost errs field named key, or "".
func fieldValue(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
