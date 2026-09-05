// Package main — the tool's own sentinels.
//
// sdkguard does not import the SDK (it must run in a consumer's tree without
// pulling the very library it audits), so it cannot use pkg/v1/errs. These are
// plain sentinels, compared by identity.
package main

import "errors"

// errProxyDisabled reports that no module proxy is reachable or permitted —
// GOPROXY=off, a proxy list with no usable URL, or a non-200 response. It is
// always handled by skipping the freshness probe, never by failing the run.
var errProxyDisabled = errors.New("sdkguard: module proxy unavailable")
