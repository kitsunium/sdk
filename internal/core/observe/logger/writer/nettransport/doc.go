// Package nettransport declares the codes and sentinels of the network log
// writer, internal/service/observe/logger/writer/nettransport: range 0.3.30.*,
// allocated to that engine (ADR 0015 service slot 0x1e) and declared here since
// ADR 0160, so the engine declares none.
//
// Package nettransport — declares the sentinels returned by the network sink's
// constructor and Write path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. No value parsed from a config map or a record is ever
// echoed into these errors (secret gate): only the network and a fixed private
// string are attached.
package nettransport
