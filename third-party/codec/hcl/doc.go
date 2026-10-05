// Package hcl wraps HashiCorp HCL v2 as a codec.Codec implementation. It lives
// under third-party/, in a module of its own (ADR 0157) — NOT
// internal/service/data/codec — because hashicorp/hcl/v2 pulls go-cty and a heavier
// dependency graph; quarantining it here keeps the dep-light SDK module — its
// service layer and its proc syscall code — untouched (ADR 0012 / ADR 0021 §Why-not /
// ADR 0022). It is opt-in: a consumer blank-imports this package to register
// the "hcl" Format; pkg/v1/data/codec does NOT pull it (the public module stays
// dep-light).
//
// HCL is decode-oriented; symmetric value-marshal is struct-only via
// gohcl.EncodeIntoBody, so the top-level value MUST be a struct (or pointer to
// one) with `hcl:"…"` field tags. Non-streaming; implements the optional Appender.
//
// Package hcl — range 0.3.37.* (ADR 0022 third-party/codec/hcl block).
//
// Package hcl — declares the sentinel *errs.Error values for the HCL codec.
package hcl
