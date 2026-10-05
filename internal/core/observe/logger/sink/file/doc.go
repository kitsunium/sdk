// Package file declares the codes and sentinels of the logger's file sink,
// internal/service/observe/logger/sink/file: range 0.3.14.*, allocated to that
// engine (ADR 0005 service/observe/logger/sink/file block) and declared here
// since ADR 0160, so the engine declares none.
//
// Package file — declares the sentinels the file sink's constructor and its
// Write / Flush / Close methods return. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.
package file
