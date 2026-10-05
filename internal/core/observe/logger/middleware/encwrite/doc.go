// Package encwrite declares the codes and sentinels of the logger's
// byte-encrypting middleware,
// internal/service/observe/logger/middleware/encwrite: range 0.3.28.*,
// allocated to that engine (ADR 0014 service slot 0x1c) and declared here since
// ADR 0160, so the engine declares none.
//
// Package encwrite — declares the sentinels returned by the byte-encrypting
// sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package encwrite
