package otlpjson

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coreotlpjson "github.com/kitsunium/sdk/internal/core/observe/logger/writer/otlpjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// OTLPLogsPath is the OTLP/HTTP logs path a collector serves.
const OTLPLogsPath string = "/v1/logs"

// rejectedFieldKey names the count of log records a partial success
// refused, on the error it returns.
const rejectedFieldKey string = "rejected_log_records"

// otlpSignal is the logs signal's vocabulary for every verdict the shared
// sender reaches: the codes of internal/core/observe/logger/writer/otlpjson.
var otlpSignal = otlp.SignalSpec{
	EndpointInvalid: coreotlpjson.OTLPLogsEndpointInvalid,
	EndpointUnparsable: errs.WrapParams{
		Code:    coreotlpjson.CodeOTLPLogsEndpointInvalid,
		Reason:  "OTLP_LOGS_ENDPOINT_INVALID",
		Public:  "The OTLP logs endpoint is not an absolute http or https URL with a path",
		Private: "service/observe/logger/writer/otlpjson: the configured OTLP logs endpoint could not be parsed as a URL",
	},
	RequestUnbuildable: errs.WrapParams{
		Code:    coreotlpjson.CodeOTLPLogsExportRejected,
		Reason:  "OTLP_LOGS_EXPORT_REJECTED",
		Public:  "The OTLP collector rejected the logs payload",
		Private: "service/observe/logger/writer/otlpjson: the OTLP/HTTP logs request could not be built",
	},
	TransportFault: errs.WrapParams{
		Code:    coreotlpjson.CodeOTLPLogsExportUnavailable,
		Reason:  "OTLP_LOGS_EXPORT_UNAVAILABLE",
		Public:  "The OTLP collector is unreachable or overloaded",
		Private: "service/observe/logger/writer/otlpjson: the OTLP/HTTP logs request failed before a response was read",
	},
	Rejected:       coreotlpjson.OTLPLogsExportRejected,
	Unavailable:    coreotlpjson.OTLPLogsExportUnavailable,
	PartialSuccess: coreotlpjson.OTLPLogsPartialSuccess,
	RejectedField:  rejectedFieldKey,
	DecodeRejected: decodeRejected,
}

// OTLPHTTPConfig configures the OTLP/HTTP logs exporter: the shared sender's
// configuration — Endpoint, Headers, Timeout, Client, MaxResponseBytes, Gzip —
// under this signal's name.
type OTLPHTTPConfig otlp.HTTPConfig

// otlpHTTPExporter posts logs documents through the shared sender.
type otlpHTTPExporter struct {
	// sender is the transport and the classification.
	sender *otlp.Sender
}

// NewOTLPHTTPExporter returns the OTLP/HTTP logs exporter: a
// corelogger.DocumentExporter and EncodedDocumentExporter posting each
// document to cfg.Endpoint (…/v1/logs) through the sender the metrics and
// trace exporters share, with their verdicts under this package's codes. A
// refused endpoint fails here, OTLPLogsEndpointInvalid.
func NewOTLPHTTPExporter(cfg OTLPHTTPConfig) (exporter corelogger.DocumentExporter, err error) {
	sender, senderErr := otlp.NewSender((*otlp.HTTPConfig)(&cfg), &otlpSignal)
	//: a refused endpoint, refused at wiring.
	if senderErr != nil {
		return nil, senderErr
	}
	//: bound.
	return &otlpHTTPExporter{sender: sender}, nil
}

// OTLPRetryable reports whether a failure of the logs exporter is the
// transient one a later attempt may cure — what forward.Config.Retryable
// wants.
func OTLPRetryable(err error) bool {
	//: the retryable class carries the unavailable code.
	return errs.HasCode(err, coreotlpjson.CodeOTLPLogsExportUnavailable)
}

// ExportDocument implements corelogger.DocumentExporter: it POSTs doc — an
// OTLP/JSON ExportLogsServiceRequest — under ctx. An empty doc posts nothing.
func (e *otlpHTTPExporter) ExportDocument(ctx context.Context, doc []byte) error {
	//: nothing to ship.
	if len(doc) == 0 {
		return nil
	}
	//: transport and classification, under the caller's bound.
	return e.sender.PostContext(ctx, doc)
}

// ExportEncodedDocument implements corelogger.EncodedDocumentExporter: doc
// posted as it is, labelled with encoding — "gzip" never decompressed, ""
// as ExportDocument, any other refused before a request (#1051).
func (e *otlpHTTPExporter) ExportEncodedDocument(ctx context.Context, doc []byte, encoding string) error {
	//: nothing to ship.
	if len(doc) == 0 {
		return nil
	}
	//: labelled as it rests.
	return e.sender.PostEncoded(ctx, doc, encoding)
}

// otlpPartialSuccess is ExportLogsPartialSuccess: rejectedLogRecords (1),
// errorMessage (2).
type otlpPartialSuccess struct {
	RejectedLogRecords otlp.LenientInt64 `json:"rejectedLogRecords"`
	ErrorMessage       string            `json:"errorMessage"`
}

// RejectedCount implements otlp.RejectedCounter.
func (p *otlpPartialSuccess) RejectedCount() int64 {
	//: the count the collector refused.
	return int64(p.RejectedLogRecords)
}

// decodeRejected reads rejectedLogRecords off a 2xx response.
func decodeRejected(payload []byte) int64 {
	var message otlpPartialSuccess
	//: the shared lenient decoding.
	return otlp.DecodeRejected(payload, &message)
}
