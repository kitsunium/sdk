// Package slogbridge — declares the package's sentinels. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package slogbridge

import "github.com/kitsunium/sdk/internal/kernel/errs"

// LoggerRequired is returned when NewHandler or New is called with a nil
// Logger. Build the destination first (logger.NewText / NewWithSink /
// Default), then bridge it — there is no "default destination" here, by the
// same reasoning that made NewText refuse to default to stderr (ADR 0002).
var LoggerRequired = errs.Define(CodeLoggerRequired, "LOGGER_REQUIRED",
	"slog bridge requires an explicit logger",
	"pkg/v1/logger/slogbridge: NewHandler/New called with a nil logger.Logger; build one with logger.NewText or logger.Default() first")
