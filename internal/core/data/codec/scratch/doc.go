// Package scratch provides shared, size-bounded recycling primitives for the
// service codecs. Every codec that encodes into a transient *bytes.Buffer
// previously declared its own sync.Pool, its own 256 KiB cap-discard constant,
// and its own release helper. Centralising them here gives a single source of
// truth for the cap-discard policy and a single shared pool whose reuse rate
// is higher than nine independent pools (sync.Pool is internally per-P
// sharded, so consolidation does not add contention).
//
// The pooling MECHANISM lives in internal/kernel/concur/recycler (ADR 0010); scratch
// is a codec-domain consumer that keeps the 256 KiB threshold and the concrete
// *bytes.Buffer / *bytes.Reader types here.
//
// Lifetime contract: a value returned by an Acquire* call is owned by the
// caller until the matching Release* call. After Release the value — and any
// slice aliasing it (e.g. buf.Bytes()) — MUST NOT be used; clone the encoded
// bytes first if they need to outlive the release.
package scratch
