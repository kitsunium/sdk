// Package slogbridge — range 1.1.1.* (ADR 0005 pkg/v1/logger/slogbridge block).
package slogbridge

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 1.1.1.0 - 1.1.1.255

// CodeLoggerRequired identifies a NewHandler / New call with a nil Logger.
// The bridge refuses to build a handler that discards silently: its whole
// purpose is to guarantee a single pipeline, and a bridge to nowhere would
// break that guarantee precisely where the caller believed it held.
const CodeLoggerRequired errs.Code = 0x01_01_01_01 // 1.1.1.1

// LoggerRequired is returned when NewHandler or New is called with a nil
// Logger. Build the destination first (logger.NewText / NewWithSink /
// Default), then bridge it — there is no "default destination" here, by the
// same reasoning that made NewText refuse to default to stderr (ADR 0002).
var LoggerRequired = errs.Define(CodeLoggerRequired, "LOGGER_REQUIRED",
	"slog bridge requires an explicit logger",
	"pkg/v1/logger/slogbridge: NewHandler/New called with a nil logger.Logger; build one with logger.NewText or logger.Default() first")
