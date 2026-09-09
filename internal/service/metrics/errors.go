// Package metrics — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// InvalidMetricName is returned when an instrument name cannot be spelled
	// as a Prometheus metric name.
	InvalidMetricName = errs.Define(CodeInvalidMetricName, "INVALID_METRIC_NAME",
		"An instrument name is not a valid Prometheus metric name",
		"service/metrics: the Prometheus exposition format requires [a-zA-Z_:][a-zA-Z0-9_:]*; rename the instrument at its call site")

	// InvalidLabelName is returned when a label key cannot be spelled as a
	// Prometheus label name.
	InvalidLabelName = errs.Define(CodeInvalidLabelName, "INVALID_LABEL_NAME",
		"A label key is not a valid Prometheus label name",
		"service/metrics: the Prometheus exposition format requires [a-zA-Z_][a-zA-Z0-9_]* for a label name — no colon, unlike a metric name")

	// ReservedLabelName is returned when a label key is legal but reserved.
	ReservedLabelName = errs.Define(CodeReservedLabelName, "RESERVED_LABEL_NAME",
		"A label key is reserved by the Prometheus exposition format",
		"service/metrics: a \"__\" prefix is reserved for the server's internal labels, and \"le\" is reserved for a histogram's bucket bound")
)
