// Package metrics — range 0.3.45.* (ADR 0005 service/metrics block).
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.45.0 - 0.3.45.255

// CodeInvalidMetricName identifies an instrument name the Prometheus text
// exposition format cannot carry: it does not match [a-zA-Z_:][a-zA-Z0-9_:]*.
const CodeInvalidMetricName errs.Code = 0x00_03_2D_01 // 0.3.45.1

// CodeInvalidLabelName identifies a label key the Prometheus text exposition
// format cannot carry: it does not match [a-zA-Z_][a-zA-Z0-9_]* (a metric name
// may hold a colon, a label name may not).
const CodeInvalidLabelName errs.Code = 0x00_03_2D_02 // 0.3.45.2

// CodeReservedLabelName identifies a syntactically legal label key that the
// exposition format or the Prometheus server reserves for its own use: the "__"
// prefix, or "le" on a histogram, where it names the bucket's upper bound.
const CodeReservedLabelName errs.Code = 0x00_03_2D_03 // 0.3.45.3
