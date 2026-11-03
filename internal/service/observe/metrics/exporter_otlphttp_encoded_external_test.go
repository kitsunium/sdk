package metrics_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// TestOTLPHTTPExportEncodedDocumentPostsGzipAsItRests (#1051): the exporter
// is an EncodedDocumentExporter; a gzip document reaches the collector as it
// rests, labelled Content-Encoding: gzip, and decodes to the document; the
// identity encoding posts the document unlabelled; an encoding it cannot
// label is refused, permanent, before any request.
func TestOTLPHTTPExportEncodedDocumentPostsGzipAsItRests(t *testing.T) {
	t.Parallel()
	type seen struct{ encoding, body string }
	got := make(chan seen, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Errorf("not gzip: %v", err)
				return
			}
			raw, _ = io.ReadAll(zr)
		}
		got <- seen{r.Header.Get("Content-Encoding"), string(raw)}
	}))
	defer server.Close()
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlphttp", svcmetrics.OTLPHTTPConfig{Endpoint: server.URL + svcmetrics.OTLPMetricsPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	encoded, ok := exporter.(coremetrics.EncodedDocumentExporter)
	if !ok {
		t.Fatal("the OTLP/HTTP exporter is not an EncodedDocumentExporter")
	}
	doc := mustEncode(t, gaugeSnapshot(nil, 2.5))
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte(doc))
	_ = zw.Close()
	if err := encoded.ExportEncodedDocument(context.Background(), zipped.Bytes(), "gzip"); err != nil {
		t.Fatalf("ExportEncodedDocument(gzip): %v", err)
	}
	if s := <-got; s.encoding != "gzip" || s.body != doc {
		t.Fatalf("collector saw %q labelled %q, want the document gzipped", s.body, s.encoding)
	}
	if err := encoded.ExportEncodedDocument(context.Background(), []byte(doc), ""); err != nil {
		t.Fatalf("ExportEncodedDocument(identity): %v", err)
	}
	if s := <-got; s.encoding != "" || s.body != doc {
		t.Fatalf("collector saw %q labelled %q, want the document unlabelled", s.body, s.encoding)
	}
	if err := encoded.ExportEncodedDocument(context.Background(), nil, "gzip"); err != nil {
		t.Fatalf("an empty document: %v", err)
	}
	err = encoded.ExportEncodedDocument(context.Background(), []byte(doc), "zstd")
	if err == nil || svcmetrics.OTLPRetryable(err) {
		t.Fatalf("an unknown encoding = %v, want a permanent refusal", err)
	}
	select {
	case s := <-got:
		t.Fatalf("a refused or empty document reached the collector: %+v", s)
	default:
	}
}
